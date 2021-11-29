package controllers_test

import (
	"context"
	"github.com/medik8s/poison-pill/controllers"
	"github.com/medik8s/poison-pill/pkg/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pprNamespace = "default"
)

var _ = Describe("ppr Controller", func() {
	ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
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

	Context("Unhealthy node without poison-pill pod", func() {
		//if the unhealthy node doesn't have the poison-pill pod
		//we don't want to delete the node, since it might never
		//be in a safe state (i.e. rebooted)

		BeforeEach(func() {
			createPPR(ppr)
		})

		AfterEach(func() {
			deletePPR(ppr)
		})

		It("ppr should not have finalizers", func() {
			testNoFinalizer(ppr)
		})
	})

	Context("Unhealthy node with poison-pill pod but unable to reboot", func() {
		//if the unhealthy node doesn't have watchdog and it's is-reboot-capable annotation is not true
		//we don't want to delete the node, since it will never
		//be in a safe state (i.e. rebooted)

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node

			BeforeEach(func() {
				createPoisonPillPod()
				createPPR(ppr)
			})

			AfterEach(func() {
				deletePoisonPillPod()
				deletePPR(ppr)
			})

			Context("node doesn't have is-reboot-capable annotation", func() {
				BeforeEach(func() {
					//remove the annotation, if exists
					deleteIsRebootCapableAnnotation()
				})

				It("ppr should not have finalizers when is-reboot-capable annotation doesn't exist", func() {
					testNoFinalizer(ppr)
				})
			})

			Context("node's is-reboot-capable annotation is false", func() {
				BeforeEach(func() {
					rebootCapableAnnotationValue := "false"
					updateIsRebootCapable(rebootCapableAnnotationValue)
				})

				It("ppr should not have finalizers when is-reboot-capable annotation is false", func() {
					testNoFinalizer(ppr)
				})
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {
		var beforePPR time.Time
		BeforeEach(func() {
			createPoisonPillPod()
			updateIsRebootCapable("true")
			beforePPR = time.Now().Add(-time.Second)
			createPPR(ppr)
		})

		AfterEach(func() {
			deletePoisonPillPod()
			deletePPR(ppr)
		})

		It("Remediation flow", func() {
			By("make sure poison pill exists with correct label")
			podList := &v1.PodList{}

			selector := labels.NewSelector()
			requirement, _ := labels.NewRequirement("app", selection.Equals, []string{"poison-pill-agent"})
			selector = selector.Add(*requirement)

			Eventually(func() int {
				Expect(k8sClient.Client.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})).To(Succeed())
				return len(podList.Items)
			}, 5*time.Second, 250*time.Millisecond).Should(Equal(1))

			By("Verify that node was marked as unschedulable")
			node := &v1.Node{}
			Eventually(func() bool {
				Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())
				return node.Spec.Unschedulable
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())

			By("Add unschedulable taint to node to simulate node controller")
			node.Spec.Taints = append(node.Spec.Taints, *controllers.NodeUnschedulableTaint)
			Expect(k8sClient.Client.Update(context.TODO(), node)).To(Succeed())

			By("Verify that time has been added to PPR status")
			Eventually(func() *metav1.Time {
				pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}

				Expect(k8sClient.Client.Get(context.TODO(), pprNamespacedName, ppr)).To(Succeed())
				return ppr.Status.TimeAssumedRebooted

			}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())

			newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
			By("Verify that node backup annotation matches the node")
			pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}

			Expect(k8sClient.Client.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
			Expect(newPpr.Status.NodeBackup).ToNot(BeNil(), "node backup should exist")
			nodeToRestore := newPpr.Status.NodeBackup

			node = &v1.Node{}
			Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())

			//todo why do we need the following 2 lines? this might be a bug
			nodeToRestore.TypeMeta.Kind = "Node"
			nodeToRestore.TypeMeta.APIVersion = "v1"
			Expect(nodeToRestore).To(Equal(node))

			By("Verify that finalizer was added")
			Expect(controllerutil.ContainsFinalizer(newPpr, controllers.PPRFinalizer)).Should(BeTrue(), "finalizer should be added")

			By("Verify that watchdog is not receiving food")
			currentLastFoodTime := dummyDog.LastFoodTime()
			Consistently(func() time.Time {
				return dummyDog.LastFoodTime()
			}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Equal(currentLastFoodTime))

			By("Verify that node has been deleted and restored")
			Eventually(func() time.Time {
				err := k8sClient.Reader.Get(context.TODO(), unhealthyNodeNamespacedName, node)
				if err != nil {
					return beforePPR
				}
				return node.CreationTimestamp.Time
			}, 10*time.Second, 200*time.Millisecond).Should(BeTemporally(">", beforePPR))

			By("Verify that node is not marked as unschedulable")
			Eventually(func() bool {
				err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
				if err != nil {
					return true
				}
				return node.Spec.Unschedulable
			}, 95*time.Second, 250*time.Millisecond).Should(BeFalse())

			By("Verify that finalizer exists until node updates status", func() {
				Consistently(func() bool {
					pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}
					newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
					Expect(k8sClient.Client.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
					return controllerutil.ContainsFinalizer(newPpr, controllers.PPRFinalizer)
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())

				By("Update node's last hearbeat time")
				//we simulate kubelet coming up, this is required to remove the finalizer
				node.Status.Conditions = make([]v1.NodeCondition, 1)
				node.Status.Conditions[0].Status = v1.ConditionTrue
				node.Status.Conditions[0].Type = v1.NodeReady
				Expect(k8sClient.Client.Status().Update(context.Background(), node)).To(Succeed())

				By("Verify that finalizer was removed and PPR can be deleted")
				Eventually(func() bool {
					pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}
					newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
					Expect(k8sClient.Client.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
					return controllerutil.ContainsFinalizer(newPpr, controllers.PPRFinalizer)
				}, 10*time.Second, 250*time.Millisecond).Should(BeFalse())
			})
		})
	})

	Context("Unhealthy node without api-server access", func() {

		// this is not a controller test anymore... it's testing peers. But keep it here for now...

		BeforeEach(func() {
			By("Simulate api-server failure")
			k8sClient.ShouldSimulateFailure = true
			createPPR(ppr)
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

func deletePPR(ppr *poisonpillv1alpha1.PoisonPillRemediation) {
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), ppr)).To(Succeed(), "failed to delete ppr CR")
}

func createPPR(ppr *poisonpillv1alpha1.PoisonPillRemediation) {
	ppr = &poisonpillv1alpha1.PoisonPillRemediation{}
	ppr.Name = unhealthyNodeName
	ppr.Namespace = pprNamespace
	ExpectWithOffset(1, k8sClient.Client.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
}

func createPoisonPillPod() {
	pod := &v1.Pod{}
	pod.Spec.NodeName = unhealthyNodeName
	pod.Labels = map[string]string{"app": "poison-pill-agent"}
	pod.Name = "poison-pill"
	pod.Namespace = namespace
	container := v1.Container{
		Name:  "foo",
		Image: "foo",
	}
	pod.Spec.Containers = []v1.Container{container}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func deletePoisonPillPod() {
	pod := &v1.Pod{}
	pod.Name = "poison-pill"
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
func testNoFinalizer(ppr *poisonpillv1alpha1.PoisonPillRemediation) {
	pprKey := client.ObjectKey{
		Namespace: pprNamespace,
		Name:      unhealthyNodeName,
	}

	EventuallyWithOffset(1, func() error {
		return k8sClient.Client.Get(context.Background(), pprKey, ppr)
	}, 10*time.Second, 200*time.Millisecond).Should(Succeed())

	ConsistentlyWithOffset(1, func() []string {
		Expect(k8sClient.Client.Get(context.Background(), pprKey, ppr)).To(Succeed())
		//if no finalizer was set, it means we didn't start remediation process
		return ppr.Finalizers
	}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
}
