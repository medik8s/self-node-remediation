package controllers_test

import (
	"context"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/utils"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	snrNamespace = "default"
)

var _ = Describe("snr Controller", func() {
	snr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	snr.Name = unhealthyNodeName
	snr.Namespace = snrNamespace

	BeforeEach(func() {
		k8sClient.ShouldSimulateFailure = false
	})

	AfterEach(func() {
		//clear node's state, this is important to remove taints, label etc.
		Expect(k8sClient.Update(context.Background(), getNode(unhealthyNodeName)))
		Expect(k8sClient.Update(context.Background(), getNode(peerNodeName)))
	})

	It("check nodes exist", func() {
		By("Check the unhealthy node exists")
		node := &v1.Node{}
		Eventually(func() error {
			return k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
		Expect(node.Name).To(Equal(unhealthyNodeName))
		Expect(node.CreationTimestamp).ToNot(BeZero())

		By("Check the peer node exists")
		node = &v1.Node{}
		Eventually(func() error {
			return k8sClient.Client.Get(context.TODO(), peerNodeNamespacedName, node)
		}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
		Expect(node.Name).To(Equal(peerNodeName))
		Expect(node.CreationTimestamp).ToNot(BeZero())
	})

	Context("Unhealthy node without self-node-remediation pod", func() {
		//if the unhealthy node doesn't have the self-node-remediation pod
		//we don't want to delete the node, since it might never
		//be in a safe state (i.e. rebooted)
		var remediationStrategy selfnoderemediationv1alpha1.RemediationStrategyType
		JustBeforeEach(func() {
			createSNR(remediationStrategy)
		})

		AfterEach(func() {
			deleteSNR(snr)
		})

		Context("ResourceDeletion strategy", func() {
			BeforeEach(func() {
				remediationStrategy = selfnoderemediationv1alpha1.ResourceDeletionRemediationStrategy
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer()
			})
		})

	})

	Context("Unhealthy node with api-server access", func() {
		var remediationStrategy selfnoderemediationv1alpha1.RemediationStrategyType
		var isSNRNeedsDeletion = true
		JustBeforeEach(func() {
			createSelfNodeRemediationPod()
			updateIsRebootCapable("true")
			createSNR(remediationStrategy)

			By("make sure self node remediation exists with correct label")
			verifySelfNodeRemediationPodExist()
		})

		AfterEach(func() {
			if isSNRNeedsDeletion {
				deleteSNR(snr)
			}
			isSNRNeedsDeletion = true
		})

		Context("ResourceDeletion strategy", func() {
			var vaName = "some-va"

			BeforeEach(func() {
				remediationStrategy = selfnoderemediationv1alpha1.ResourceDeletionRemediationStrategy
				createVolumeAttachment(vaName)
			})

			AfterEach(func() {
				//no need to delete pp pod or va as it was already deleted by the controller
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				addUnschedulableTaint(node)

				verifyTimeHasBeenRebootedExists()

				verifyNodeBackup()

				verifyNoWatchdogFood()

				verifySelfNodeRemediationPodDoesntExist()

				verifyVaDeleted(vaName)

				verifyFinalizerExists()

				verifyNoExecuteTaintExist()

				deleteSNR(snr)
				isSNRNeedsDeletion = false

				verifyNodeIsSchedulable()

				removeUnschedulableTaint()

				verifyNoExecuteTaintRemoved()

				verifySNRDoesNotExists()

			})
		})
	})

})

func createVolumeAttachment(vaName string) {
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vaName,
			Namespace: namespace,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "foo",
			Source:   storagev1.VolumeAttachmentSource{},
			NodeName: unhealthyNodeName,
		},
	}
	foo := "foo"
	va.Spec.Source.PersistentVolumeName = &foo
	ExpectWithOffset(1, k8sClient.Create(context.Background(), va)).To(Succeed())
}

func verifyVaDeleted(vaName string) {
	vaKey := client.ObjectKey{
		Namespace: namespace,
		Name:      vaName,
	}

	EventuallyWithOffset(1, func() bool {
		va := &storagev1.VolumeAttachment{}
		err := k8sClient.Get(context.Background(), vaKey, va)
		return apierrors.IsNotFound(err)

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifySelfNodeRemediationPodDoesntExist() {
	By("Verify that self node remediation pod has been deleted as part of the remediation")
	podKey := client.ObjectKey{
		Namespace: namespace,
		Name:      "self-node-remediation",
	}

	EventuallyWithOffset(1, func() bool {
		pod := &v1.Pod{}
		err := k8sClient.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifyNodeIsSchedulable() {
	By("Verify that node is not marked as unschedulable")
	node := &v1.Node{}
	Eventually(func() (bool, error) {
		err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.Spec.Unschedulable, err
	}, 95*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func verifyNoWatchdogFood() {
	By("Verify that watchdog is not receiving food")
	currentLastFoodTime := dummyDog.LastFoodTime()
	ConsistentlyWithOffset(1, func() time.Time {
		return dummyDog.LastFoodTime()
	}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Equal(currentLastFoodTime))
}

func verifyFinalizerExists() {
	By("Verify that finalizer was added")
	snr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), snrNamespacedName, snr)).To(Succeed())
	ExpectWithOffset(1, controllerutil.ContainsFinalizer(snr, controllers.SNRFinalizer)).Should(BeTrue(), "finalizer should be added")
}

func verifyNoExecuteTaintRemoved() {
	By("Verify that node does not have NoExecute taint")
	Eventually(isNoExecuteTaintExist, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyNoExecuteTaintExist() {
	By("Verify that node has NoExecute taint")
	Eventually(isNoExecuteTaintExist, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func isNoExecuteTaintExist() (bool, error) {
	node := &v1.Node{}
	err := k8sClient.Reader.Get(context.TODO(), unhealthyNodeNamespacedName, node)
	if err != nil {
		return false, err
	}
	for _, taint := range node.Spec.Taints {
		if controllers.NodeNoExecuteTaint.MatchTaint(&taint) {
			return true, nil
		}
	}
	return false, nil
}

// verifies that snr node backup equals to the actual node
func verifyNodeBackup() {
	By("Verify that node backup annotation matches the node")
	newSnr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}

	ExpectWithOffset(1, k8sClient.Client.Get(context.TODO(), snrNamespacedName, newSnr)).To(Succeed())
	ExpectWithOffset(1, newSnr.Status.NodeBackup).ToNot(BeNil(), "node backup should exist")
	nodeToRestore := newSnr.Status.NodeBackup

	node := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())

	//todo why do we need the following 2 lines? this might be a bug
	nodeToRestore.TypeMeta.Kind = "Node"
	nodeToRestore.TypeMeta.APIVersion = "v1"
	ExpectWithOffset(1, nodeToRestore).To(Equal(node))
}

func verifyTimeHasBeenRebootedExists() {
	By("Verify that time has been added to SNR status")
	snr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	EventuallyWithOffset(1, func() (*metav1.Time, error) {
		snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
		err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr)
		return snr.Status.TimeAssumedRebooted, err

	}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
}

func verifySNRDoesNotExists() {
	By("Verify that SNR does not exit")
	Eventually(func() bool {
		snr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
		snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
		err := k8sClient.Get(context.Background(), snrNamespacedName, snr)
		return apierrors.IsNotFound(err)
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func addUnschedulableTaint(node *v1.Node) {
	By("Add unschedulable taint to node to simulate node controller")
	node.Spec.Taints = append(node.Spec.Taints, *controllers.NodeUnschedulableTaint)
	ExpectWithOffset(1, k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
}

func removeUnschedulableTaint() {
	By("Removing unschedulable taint to node to simulate node controller")
	updateNodeFund := func(node *v1.Node) {
		taints, _ := utils.DeleteTaint(node.Spec.Taints, controllers.NodeUnschedulableTaint)
		node.Spec.Taints = taints
	}
	eventuallyUpdateNode(updateNodeFund, false)
}

func verifyNodeIsUnschedulable() *v1.Node {
	By("Verify that node was marked as unschedulable")
	node := &v1.Node{}
	Eventually(func() (bool, error) {
		err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.Spec.Unschedulable, err
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
	return node
}

func verifySelfNodeRemediationPodExist() {
	podList := &v1.PodList{}
	selector := labels.NewSelector()
	requirement, _ := labels.NewRequirement("app", selection.Equals, []string{"self-node-remediation-agent"})
	selector = selector.Add(*requirement)

	EventuallyWithOffset(1, func() (int, error) {
		err := k8sClient.Client.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
		return len(podList.Items), err
	}, 5*time.Second, 250*time.Millisecond).Should(Equal(1))
}

func deleteSNR(snr *selfnoderemediationv1alpha1.SelfNodeRemediation) {
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), snr)).To(Succeed(), "failed to delete snr CR")
}

func createSNR(strategy selfnoderemediationv1alpha1.RemediationStrategyType) {
	snr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	snr.Name = unhealthyNodeName
	snr.Namespace = snrNamespace
	snr.Spec.RemediationStrategy = strategy
	ExpectWithOffset(1, k8sClient.Client.Create(context.TODO(), snr)).To(Succeed(), "failed to create snr CR")
}

func createSelfNodeRemediationPod() {
	pod := &v1.Pod{}
	pod.Spec.NodeName = unhealthyNodeName
	pod.Labels = map[string]string{"app": "self-node-remediation-agent"}
	pod.Name = "self-node-remediation"
	pod.Namespace = namespace
	container := v1.Container{
		Name:  "foo",
		Image: "foo",
	}
	pod.Spec.Containers = []v1.Container{container}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func updateIsRebootCapable(rebootCapableAnnotationValue string) {
	unhealthyNodeKey := types.NamespacedName{
		Name: unhealthyNodeName,
	}
	node := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, node)).To(Succeed())
	patch := client.MergeFrom(node.DeepCopy())
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	if rebootCapableAnnotationValue != "" {
		node.Annotations[utils.IsRebootCapableAnnotation] = rebootCapableAnnotationValue
	}

	ExpectWithOffset(1, k8sClient.Client.Patch(context.Background(), node, patch)).To(Succeed())
}

//testNoFinalizer checks that snr doesn't have finalizer
func testNoFinalizer() {
	snr := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	snrKey := client.ObjectKey{
		Namespace: snrNamespace,
		Name:      unhealthyNodeName,
	}

	EventuallyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), snrKey, snr)
		return snr.Finalizers, err
	}, 10*time.Second, 200*time.Millisecond).Should(BeEmpty())

	ConsistentlyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), snrKey, snr)
		//if no finalizer was set, it means we didn't start remediation process
		return snr.Finalizers, err
	}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
}

func eventuallyUpdateNode(updateFunc func(*v1.Node), isStatusUpdate bool) {
	By("Verify that node was updated successfully")

	EventuallyWithOffset(1, func() error {
		node := &v1.Node{}
		if err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node); err != nil {
			return err
		}
		updateFunc(node)
		if isStatusUpdate {
			return k8sClient.Client.Status().Update(context.TODO(), node)
		}
		return k8sClient.Client.Update(context.TODO(), node)
	}, 5*time.Second, 250*time.Millisecond).Should(Succeed())

}
