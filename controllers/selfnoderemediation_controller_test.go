package controllers_test

import (
	"context"
	"k8s.io/apimachinery/pkg/api/meta"
	"time"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

const (
	snrNamespace = "default"
)

var _ = Describe("snr Controller", func() {
	snr := &v1alpha1.SelfNodeRemediation{}
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
		var remediationStrategy v1alpha1.RemediationStrategyType
		JustBeforeEach(func() {
			createSNR(remediationStrategy)
		})

		AfterEach(func() {
			deleteSNR(snr)
		})

		Context("ResourceDeletion strategy", func() {
			BeforeEach(func() {
				remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer()
			})
		})

		Context("OutOfServiceTaint strategy", func() {
			BeforeEach(func() {
				remediationStrategy = v1alpha1.OutOfServiceTaintRemediationStrategy
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer()
			})
		})
	})

	Context("Unhealthy node with self-node-remediation pod but unable to reboot", func() {
		//if the unhealthy node doesn't have watchdog and it's is-reboot-capable annotation is not true
		//we don't want to delete the node, since it will never
		//be in a safe state (i.e. rebooted)

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node

			BeforeEach(func() {
				createSelfNodeRemediationPod()
				createSNR(v1alpha1.ResourceDeletionRemediationStrategy)
			})

			AfterEach(func() {
				deleteSelfNodeRemediationPod()
				deleteSNR(snr)
			})

			Context("node doesn't have is-reboot-capable annotation", func() {
				BeforeEach(func() {
					//remove the annotation, if exists
					deleteIsRebootCapableAnnotation()
				})

				It("snr should not have finalizers when is-reboot-capable annotation doesn't exist", func() {
					testNoFinalizer()
				})
			})

			Context("node's is-reboot-capable annotation is false", func() {
				BeforeEach(func() {
					rebootCapableAnnotationValue := "false"
					updateIsRebootCapable(rebootCapableAnnotationValue)
				})

				It("snr should not have finalizers when is-reboot-capable annotation is false", func() {
					testNoFinalizer()
				})
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {
		var remediationStrategy v1alpha1.RemediationStrategyType
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
				remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
				createVolumeAttachment(vaName)
			})

			AfterEach(func() {
				//no need to delete pp pod or va as it was already deleted by the controller
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				addUnschedulableTaint(node)

				verifyProcessingCondition(metav1.ConditionTrue)

				verifyTimeHasBeenRebootedExists()

				verifyNoWatchdogFood()

				verifySelfNodeRemediationPodDoesntExist()

				verifyVaDeleted(vaName)

				verifyFinalizerExists()

				verifyNoExecuteTaintExist()

				verifyProcessingCondition(metav1.ConditionFalse)

				deleteSNR(snr)
				isSNRNeedsDeletion = false

				verifyNodeIsSchedulable()

				removeUnschedulableTaint()

				verifyNoExecuteTaintRemoved()

				verifySNRDoesNotExists()

			})

			It("The snr agent attempts to keep deleting node resources during temporary api-server failure", func() {
				node := verifyNodeIsUnschedulable()

				k8sClient.ShouldSimulateVaFailure = true

				addUnschedulableTaint(node)

				verifyProcessingCondition(metav1.ConditionTrue)

				verifyTimeHasBeenRebootedExists()

				verifyNoWatchdogFood()

				verifySelfNodeRemediationPodDoesntExist()

				verifyVaNotDeleted(vaName)

				// The kube-api calls for VA fail intentionally. In this case, we expect the snr agent to try
				// to delete node resources again. So LastError is set to the error every time Reconcile()
				// is triggered. If it becomes another error, it means something unintended happens.
				verifyLastErrorKeepsApiErrorForVa()

				k8sClient.ShouldSimulateVaFailure = false

				deleteVolumeAttachment(vaName)

				deleteSNR(snr)

				isSNRNeedsDeletion = false

				removeUnschedulableTaint()

				verifySNRDoesNotExists()

			})

		})

		Context("OutOfServiceTaint strategy", func() {
			var vaName = "some-va"

			BeforeEach(func() {
				remediationStrategy = v1alpha1.OutOfServiceTaintRemediationStrategy

				createVolumeAttachment(vaName)
			})

			AfterEach(func() {
				//no need to delete pp pod or va as it was already deleted by the controller
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				addUnschedulableTaint(node)

				verifyProcessingCondition(metav1.ConditionTrue)

				// The normal NoExecute taint tries to delete pods, however it can't delete pods
				// with stateful workloads like volumes and they are stuck in terminating status.
				createTerminatingPod()

				verifyTimeHasBeenRebootedExists()

				verifyNoWatchdogFood()

				verifyFinalizerExists()

				verifyNoExecuteTaintExist()

				verifyOutOfServiceTaintExist()

				// simulate the out-of-service taint by Pod GC Controller
				deleteTerminatingPod()
				deleteVolumeAttachment(vaName)

				verifyOutOfServiceTaintRemoved()

				verifyProcessingCondition(metav1.ConditionFalse)

				deleteSNR(snr)
				isSNRNeedsDeletion = false

				verifyNodeIsSchedulable()

				removeUnschedulableTaint()

				verifyNoExecuteTaintRemoved()

				verifySNRDoesNotExists()

			})
		})

	})

	Context("Unhealthy node without api-server access", func() {

		// this is not a controller test anymore... it's testing peers. But keep it here for now...

		BeforeEach(func() {
			By("Simulate api-server failure")
			k8sClient.ShouldSimulateFailure = true
			createSNR(v1alpha1.ResourceDeletionRemediationStrategy)
		})

		AfterEach(func() {
			deleteSNR(snr)
		})

		It("Verify that watchdog is not receiving food after some time", func() {
			lastFoodTime := dummyDog.LastFoodTime()
			timeout := dummyDog.GetTimeout()
			Eventually(func() bool {
				newTime := dummyDog.LastFoodTime()
				// ensure the timeout passed
				timeoutPassed := time.Now().After(lastFoodTime.Add(3 * timeout))
				// ensure wd wasn't feeded
				missedFeed := newTime.Before(lastFoodTime.Add(timeout))
				if timeoutPassed && missedFeed {
					return true
				}
				lastFoodTime = newTime
				return false
			}, 10*peerUpdateInterval, timeout).Should(BeTrue())
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

func verifyProcessingCondition(conditionStatus metav1.ConditionStatus) {
	By("Verify that SNR Processing status condition is correct")
	snr := &v1alpha1.SelfNodeRemediation{}
	Eventually(func() bool {
		snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}

		return meta.IsStatusConditionPresentAndEqual(snr.Status.Conditions, v1alpha1.SnrConditionProcessing, conditionStatus)

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func deleteVolumeAttachment(vaName string) {
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vaName,
			Namespace: namespace,
		},
	}
	ExpectWithOffset(1, k8sClient.Delete(context.Background(), va)).To(Succeed())
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

func verifyVaNotDeleted(vaName string) {
	vaKey := client.ObjectKey{
		Namespace: namespace,
		Name:      vaName,
	}

	ConsistentlyWithOffset(1, func() bool {
		va := &storagev1.VolumeAttachment{}
		err := k8sClient.Get(context.Background(), vaKey, va)
		return apierrors.IsNotFound(err)

	}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func verifyLastErrorKeepsApiErrorForVa() {
	By("Verify that LastError in SNR status has been kept kube-api error for VA")
	snr := &v1alpha1.SelfNodeRemediation{}
	ConsistentlyWithOffset(1, func() bool {
		snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		return snr.Status.LastError == k8sClient.VaFailureMessage
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
	snr := &v1alpha1.SelfNodeRemediation{}
	snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), snrNamespacedName, snr)).To(Succeed())
	ExpectWithOffset(1, controllerutil.ContainsFinalizer(snr, controllers.SNRFinalizer)).Should(BeTrue(), "finalizer should be added")
}

func verifyNoExecuteTaintRemoved() {
	By("Verify that node does not have NoExecute taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.NodeNoExecuteTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyNoExecuteTaintExist() {
	By("Verify that node has NoExecute taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.NodeNoExecuteTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func verifyOutOfServiceTaintRemoved() {
	By("Verify that node does not have out-of-service taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.OutOfServiceTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyOutOfServiceTaintExist() {
	By("Verify that node has out-of-service taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controllers.OutOfServiceTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func isTaintExist(taintToMatch *v1.Taint) (bool, error) {
	node := &v1.Node{}
	err := k8sClient.Reader.Get(context.TODO(), unhealthyNodeNamespacedName, node)
	if err != nil {
		return false, err
	}
	for _, taint := range node.Spec.Taints {
		if taintToMatch.MatchTaint(&taint) {
			return true, nil
		}
	}
	return false, nil
}

func verifyTimeHasBeenRebootedExists() {
	By("Verify that time has been added to SNR status")
	snr := &v1alpha1.SelfNodeRemediation{}
	EventuallyWithOffset(1, func() (*metav1.Time, error) {
		snrNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: snrNamespace}
		err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr)
		return snr.Status.TimeAssumedRebooted, err

	}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
}

func verifySNRDoesNotExists() {
	By("Verify that SNR does not exit")
	Eventually(func() bool {
		snr := &v1alpha1.SelfNodeRemediation{}
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

func deleteSNR(snr *v1alpha1.SelfNodeRemediation) {
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), snr)).To(Succeed(), "failed to delete snr CR")
}

func createSNR(strategy v1alpha1.RemediationStrategyType) {
	snr := &v1alpha1.SelfNodeRemediation{}
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

func deleteSelfNodeRemediationPod() {
	pod := &v1.Pod{}
	pod.Name = "self-node-remediation"
	pod.Namespace = namespace
	podKey := client.ObjectKey{
		Namespace: namespace,
		Name:      pod.Name,
	}

	var grace client.GracePeriodSeconds = 0
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod, grace)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Client.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
}

func createTerminatingPod() {
	pod := &v1.Pod{}
	pod.Spec.NodeName = unhealthyNodeName
	pod.Name = "terminatingpod"
	pod.Namespace = "default"
	container := v1.Container{
		Name:  "bar",
		Image: "bar",
	}
	pod.Spec.Containers = []v1.Container{container}
	now := metav1.Now()
	pod.ObjectMeta = metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, DeletionTimestamp: &now}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func deleteTerminatingPod() {
	pod := &v1.Pod{}
	pod.Name = "terminatingpod"
	pod.Namespace = "default"
	podKey := client.ObjectKey{
		Namespace: namespace,
		Name:      pod.Name,
	}

	var grace client.GracePeriodSeconds = 0
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod, grace)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Client.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
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

func deleteIsRebootCapableAnnotation() {
	unhealthyNodeKey := types.NamespacedName{
		Name: unhealthyNodeName,
	}
	unhealthyNode := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, unhealthyNode)).To(Succeed())
	patch := client.MergeFrom(unhealthyNode.DeepCopy())
	if unhealthyNode.Annotations != nil {
		delete(unhealthyNode.Annotations, utils.IsRebootCapableAnnotation)
	}

	ExpectWithOffset(1, k8sClient.Client.Patch(context.Background(), unhealthyNode, patch)).To(Succeed())
}

// testNoFinalizer checks that snr doesn't have finalizer
func testNoFinalizer() {
	snr := &v1alpha1.SelfNodeRemediation{}
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
