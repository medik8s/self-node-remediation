package controllers_test

import (
	"context"
	"github.com/medik8s/self-node/controllers"
	"github.com/medik8s/self-node/pkg/utils"
	"k8s.io/api/storage/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	selfnodev1alpha1 "github.com/medik8s/self-node/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pprNamespace = "default"
)

var _ = Describe("ppr Controller", func() {
	ppr := &selfnodev1alpha1.SelfNodeRemediation{}
	ppr.Name = unhealthyNodeName
	ppr.Namespace = pprNamespace

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

	Context("Unhealthy node without self-node pod", func() {
		//if the unhealthy node doesn't have the self-node pod
		//we don't want to delete the node, since it might never
		//be in a safe state (i.e. rebooted)
		var remediationStrategy selfnodev1alpha1.RemediationStrategyType
		JustBeforeEach(func() {
			createPPR(ppr, remediationStrategy)
		})

		AfterEach(func() {
			deletePPR(ppr)
		})

		Context("NodeDeletion strategy", func() {
			BeforeEach(func() {
				remediationStrategy = selfnodev1alpha1.NodeDeletionRemediationStrategy
			})

			It("ppr should not have finalizers", func() {
				testNoFinalizer()
			})
		})

		Context("ResourceDeletion strategy", func() {
			BeforeEach(func() {
				remediationStrategy = selfnodev1alpha1.ResourceDeletionRemediationStrategy
			})

			It("ppr should not have finalizers", func() {
				testNoFinalizer()
			})
		})

	})

	Context("Unhealthy node with self-node pod but unable to reboot", func() {
		//if the unhealthy node doesn't have watchdog and it's is-reboot-capable annotation is not true
		//we don't want to delete the node, since it will never
		//be in a safe state (i.e. rebooted)

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node

			BeforeEach(func() {
				createSelfNodePod()
				createPPR(ppr, selfnodev1alpha1.NodeDeletionRemediationStrategy)
			})

			AfterEach(func() {
				deleteSelfNodePod()
				deletePPR(ppr)
			})

			Context("node doesn't have is-reboot-capable annotation", func() {
				BeforeEach(func() {
					//remove the annotation, if exists
					deleteIsRebootCapableAnnotation()
				})

				It("ppr should not have finalizers when is-reboot-capable annotation doesn't exist", func() {
					testNoFinalizer()
				})
			})

			Context("node's is-reboot-capable annotation is false", func() {
				BeforeEach(func() {
					rebootCapableAnnotationValue := "false"
					updateIsRebootCapable(rebootCapableAnnotationValue)
				})

				It("ppr should not have finalizers when is-reboot-capable annotation is false", func() {
					testNoFinalizer()
				})
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {
		var beforePPR time.Time
		var remediationStrategy selfnodev1alpha1.RemediationStrategyType

		JustBeforeEach(func() {
			createSelfNodePod()
			updateIsRebootCapable("true")
			beforePPR = time.Now().Add(-time.Second)
			createPPR(ppr, remediationStrategy)

			By("make sure self node exists with correct label")
			verifySelfNodePodExist()
		})

		AfterEach(func() {
			deletePPR(ppr)
		})

		Context("NodeDeletion strategy", func() {
			BeforeEach(func() {
				remediationStrategy = selfnodev1alpha1.NodeDeletionRemediationStrategy
			})

			AfterEach(func() {
				deleteSelfNodePod()
			})

			It("Remediation flow", func() {
				node := verifyNodeIsUnschedulable()

				addUnschedulableTaint(node)

				verifyTimeHasBeenRebootedExists()

				verifyNodeBackup()

				verifyFinalizerExists()

				verifyNoWatchdogFood()

				verifyNodeDeletedAndRestored(beforePPR)

				verifyNodeIsSchedulable()

				By("Verify that finalizer exists until node updates status")
				Consistently(func() error {
					verifyFinalizerExists()
					return nil
				}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

				By("Update node's last hearbeat time")
				//we simulate kubelet coming up, this is required to remove the finalizer
				node = &v1.Node{}
				Expect(k8sClient.Get(context.Background(), unhealthyNodeNamespacedName, node)).To(Succeed())
				node.Status.Conditions = make([]v1.NodeCondition, 1)
				node.Status.Conditions[0].Status = v1.ConditionTrue
				node.Status.Conditions[0].Type = v1.NodeReady
				node.Status.Conditions[0].LastTransitionTime = metav1.Now()
				node.Status.Conditions[0].LastHeartbeatTime = metav1.Now()
				node.Status.Conditions[0].Reason = "foo"
				Expect(k8sClient.Client.Status().Update(context.Background(), node)).To(Succeed())

				By("Verify that finalizer was removed and PPR can be deleted")
				testNoFinalizer()

			})
		})

		Context("ResourceDeletion strategy", func() {
			var vaName = "some-va"

			BeforeEach(func() {
				remediationStrategy = selfnodev1alpha1.ResourceDeletionRemediationStrategy
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

				verifyFinalizerExists()

				verifyNoWatchdogFood()

				verifySelfNodePodDoesntExist()

				verifyVaDeleted(vaName)

				verifyNodeIsSchedulable()

				By("Verify that finalizer was removed and PPR can be deleted")
				testNoFinalizer()

			})
		})
	})

	Context("Unhealthy node without api-server access", func() {

		// this is not a controller test anymore... it's testing peers. But keep it here for now...

		BeforeEach(func() {
			By("Simulate api-server failure")
			k8sClient.ShouldSimulateFailure = true
			createPPR(ppr, selfnodev1alpha1.NodeDeletionRemediationStrategy)
		})

		AfterEach(func() {
			deletePPR(ppr)
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
	va := &v1beta1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vaName,
			Namespace: namespace,
		},
		Spec: v1beta1.VolumeAttachmentSpec{
			Attacher: "foo",
			Source:   v1beta1.VolumeAttachmentSource{},
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
		va := &v1beta1.VolumeAttachment{}
		err := k8sClient.Get(context.Background(), vaKey, va)
		return apierrors.IsNotFound(err)

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifySelfNodePodDoesntExist() {
	By("Verify that self node pod has been deleted as part of the remediation")
	podKey := client.ObjectKey{
		Namespace: namespace,
		Name:      "self-node",
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

func verifyNodeDeletedAndRestored(beforePPR time.Time) {
	By("Verify that node has been deleted and restored")
	node := &v1.Node{}
	Eventually(func() (time.Time, error) {
		err := k8sClient.Reader.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		return node.CreationTimestamp.Time, err
	}, 10*time.Second, 200*time.Millisecond).Should(BeTemporally(">", beforePPR))
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
	ppr := &selfnodev1alpha1.SelfNodeRemediation{}
	pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), pprNamespacedName, ppr)).To(Succeed())
	ExpectWithOffset(1, controllerutil.ContainsFinalizer(ppr, controllers.PPRFinalizer)).Should(BeTrue(), "finalizer should be added")
}

// verifies that ppr node backup equals to the actual node
func verifyNodeBackup() {
	By("Verify that node backup annotation matches the node")
	newPpr := &selfnodev1alpha1.SelfNodeRemediation{}
	pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}

	ExpectWithOffset(1, k8sClient.Client.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
	ExpectWithOffset(1, newPpr.Status.NodeBackup).ToNot(BeNil(), "node backup should exist")
	nodeToRestore := newPpr.Status.NodeBackup

	node := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())

	//todo why do we need the following 2 lines? this might be a bug
	nodeToRestore.TypeMeta.Kind = "Node"
	nodeToRestore.TypeMeta.APIVersion = "v1"
	ExpectWithOffset(1, nodeToRestore).To(Equal(node))
}

func verifyTimeHasBeenRebootedExists() {
	By("Verify that time has been added to PPR status")
	ppr := &selfnodev1alpha1.SelfNodeRemediation{}
	EventuallyWithOffset(1, func() (*metav1.Time, error) {
		pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}
		err := k8sClient.Client.Get(context.Background(), pprNamespacedName, ppr)
		return ppr.Status.TimeAssumedRebooted, err

	}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
}

func addUnschedulableTaint(node *v1.Node) {
	By("Add unschedulable taint to node to simulate node controller")
	node.Spec.Taints = append(node.Spec.Taints, *controllers.NodeUnschedulableTaint)
	ExpectWithOffset(1, k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
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

func verifySelfNodePodExist() {
	podList := &v1.PodList{}
	selector := labels.NewSelector()
	requirement, _ := labels.NewRequirement("app", selection.Equals, []string{"self-node-agent"})
	selector = selector.Add(*requirement)

	EventuallyWithOffset(1, func() (int, error) {
		err := k8sClient.Client.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
		return len(podList.Items), err
	}, 5*time.Second, 250*time.Millisecond).Should(Equal(1))
}

func deletePPR(ppr *selfnodev1alpha1.SelfNodeRemediation) {
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), ppr)).To(Succeed(), "failed to delete ppr CR")
}

func createPPR(ppr *selfnodev1alpha1.SelfNodeRemediation, strategy selfnodev1alpha1.RemediationStrategyType) {
	ppr = &selfnodev1alpha1.SelfNodeRemediation{}
	ppr.Name = unhealthyNodeName
	ppr.Namespace = pprNamespace
	ppr.Spec.RemediationStrategy = strategy
	ExpectWithOffset(1, k8sClient.Client.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
}

func createSelfNodePod() {
	pod := &v1.Pod{}
	pod.Spec.NodeName = unhealthyNodeName
	pod.Labels = map[string]string{"app": "self-node-agent"}
	pod.Name = "self-node"
	pod.Namespace = namespace
	container := v1.Container{
		Name:  "foo",
		Image: "foo",
	}
	pod.Spec.Containers = []v1.Container{container}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func deleteSelfNodePod() {
	pod := &v1.Pod{}
	pod.Name = "self-node"
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

func updateIsRebootCapable(rebootCapableAnnotationValue string) {
	unhealthyNodeKey := types.NamespacedName{
		Name: unhealthyNodeName,
	}
	node := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, node)).To(Succeed())
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	if rebootCapableAnnotationValue != "" {
		node.Annotations[utils.IsRebootCapableAnnotation] = rebootCapableAnnotationValue
	}

	ExpectWithOffset(1, k8sClient.Client.Update(context.Background(), node)).To(Succeed())
}

func deleteIsRebootCapableAnnotation() {
	unhealthyNodeKey := types.NamespacedName{
		Name: unhealthyNodeName,
	}
	unhealthyNode := &v1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, unhealthyNode)).To(Succeed())

	if unhealthyNode.Annotations != nil {
		delete(unhealthyNode.Annotations, utils.IsRebootCapableAnnotation)
	}

	ExpectWithOffset(1, k8sClient.Client.Update(context.Background(), unhealthyNode)).To(Succeed())
}

//testNoFinalizer checks that ppr doesn't have finalizer
func testNoFinalizer() {
	ppr := &selfnodev1alpha1.SelfNodeRemediation{}
	pprKey := client.ObjectKey{
		Namespace: pprNamespace,
		Name:      unhealthyNodeName,
	}

	EventuallyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), pprKey, ppr)
		return ppr.Finalizers, err
	}, 10*time.Second, 200*time.Millisecond).Should(BeEmpty())

	ConsistentlyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), pprKey, ppr)
		//if no finalizer was set, it means we didn't start remediation process
		return ppr.Finalizers, err
	}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
}
