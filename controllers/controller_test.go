package controllers_test

import (
	"context"
	"k8s.io/apimachinery/pkg/types"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/controllers"
)

const (
	unhealthyNodeName         = "node1"
	peerNodeName              = "node2"
	pprNamespace              = "default"
	isRebootCapableAnnotation = "is-reboot-capable.poison-pill.medik8s.io"
)

var _ = Describe("ppr Controller", func() {

	unhealthyNodeNamespacedName := client.ObjectKey{
		Name:      unhealthyNodeName,
		Namespace: "",
	}
	peerNodeNamespacedName := client.ObjectKey{
		Name:      peerNodeName,
		Namespace: "",
	}

	ppr := &poisonpillv1alpha1.PoisonPillRemediation{}

	Context("Unhealthy node without poison-pill pod", func() {
		//if the unhealthy node doesn't have the poison-pill pod
		//we don't want to delete the node, since it might never
		//be in a safe state (i.e. rebooted)

		BeforeEach(func() {
			ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
			ppr.Name = unhealthyNodeName
			ppr.Namespace = pprNamespace

			Expect(k8sClient.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(context.Background(), ppr)).To(Succeed(), "failed to delete ppr CR")
		})

		It("ppr should not have finalizers", func() {
			pprKey := client.ObjectKey{
				Namespace: pprNamespace,
				Name:      unhealthyNodeName,
			}
			Eventually(func() error {
				return k8sClient.Get(context.Background(), pprKey, ppr)
			}, 10*time.Second, 200*time.Millisecond).Should(Succeed())

			Consistently(func() []string {
				Expect(k8sClient.Get(context.Background(), pprKey, ppr)).To(Succeed())
				//if no finalizer was set, it means we didn't start remediation process
				return ppr.Finalizers
			}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
		})
	})

	Context("Unhealthy node with poison-pill pod but unable to reboot", func() {
		//if the unhealthy node doesn't have watchdog and it's is-reboot-capable label is unknown or false
		//we don't want to delete the node, since it will never
		//be in a safe state (i.e. rebooted)

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node
			rebootCapableLabelValue := ""

			JustBeforeEach(func() {
				updateIsRebootCapable(rebootCapableLabelValue)
			})

			It("create poison pill pod", func() {
				//create poison pill pod
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
				Expect(k8sClient.Create(context.Background(), pod)).To(Succeed())

				ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
				ppr.Name = unhealthyNodeName
				ppr.Namespace = pprNamespace
			})

			BeforeEach(func() {
				ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
				ppr.Name = unhealthyNodeName
				ppr.Namespace = pprNamespace

				Expect(k8sClient.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
			})

			AfterEach(func() {
				Expect(k8sClient.Delete(context.Background(), ppr)).To(Succeed(), "failed to delete ppr CR")
			})

			It("ppr should not have finalizers when is-reboot-capable label doesn't exist", func() {
				pprKey := client.ObjectKey{
					Namespace: pprNamespace,
					Name:      unhealthyNodeName,
				}

				Eventually(func() error {
					return k8sClient.Get(context.Background(), pprKey, ppr)
				}, 10*time.Second, 200*time.Millisecond).Should(Succeed())

				Consistently(func() []string {
					Expect(k8sClient.Get(context.Background(), pprKey, ppr)).To(Succeed())
					//if no finalizer was set, it means we didn't start remediation process
					return ppr.Finalizers
				}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
			})

			BeforeEach(func() {
				rebootCapableLabelValue = "false"
			})

			It("ppr should not have finalizers when is-reboot-capable label is false", func() {
				pprKey := client.ObjectKey{
					Namespace: pprNamespace,
					Name:      unhealthyNodeName,
				}

				Eventually(func() error {
					return k8sClient.Get(context.Background(), pprKey, ppr)
				}, 10*time.Second, 200*time.Millisecond).Should(Succeed())

				Consistently(func() []string {
					Expect(k8sClient.Get(context.Background(), pprKey, ppr)).To(Succeed())
					//if no finalizer was set, it means we didn't start remediation process
					return ppr.Finalizers
				}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {

		It("Disable api-server failure", func() {
			k8sClient.ShouldSimulateFailure = false
		})

		It("Check the unhealthy node exists", func() {
			node := &v1.Node{}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), unhealthyNodeNamespacedName, node)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
			Expect(node.Name).To(Equal(unhealthyNodeName))
			Expect(node.CreationTimestamp).ToNot(BeZero())
		})

		It("mark unhealthy node as reboot capable", func() {
			updateIsRebootCapable("true")
		})

		It("Check the peer node exists", func() {
			node := &v1.Node{}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), peerNodeNamespacedName, node)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
			Expect(node.Name).To(Equal(peerNodeName))
			Expect(node.CreationTimestamp).ToNot(BeZero())
		})

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node
			BeforeEach(func() {

			})

			It("poison pill agent pod should exist", func() {
				podList := &v1.PodList{}

				selector := labels.NewSelector()
				requirement, _ := labels.NewRequirement("app", selection.Equals, []string{"poison-pill-agent"})
				selector = selector.Add(*requirement)

				Eventually(func() int {
					Expect(k8sClient.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})).To(Succeed())
					return len(podList.Items)
				}, 5*time.Second, 250*time.Millisecond).Should(Equal(1))

			})
		})

		beforePPR := time.Now()

		It("Create ppr for unhealthy node", func() {
			ppr = &poisonpillv1alpha1.PoisonPillRemediation{}
			ppr.Name = unhealthyNodeName
			ppr.Namespace = pprNamespace
			Expect(k8sClient.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
		})

		node := &v1.Node{}
		It("Verify that node was marked as unschedulable ", func() {
			Eventually(func() bool {
				Expect(k8sClient.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())
				return node.Spec.Unschedulable
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		It("Add unschedulable taint to node to simulate node controller", func() {
			node.Spec.Taints = append(node.Spec.Taints, *controllers.NodeUnschedulableTaint)
			Expect(k8sClient.Update(context.TODO(), node)).To(Succeed())
		})

		It("Verify that time has been added to PPR status", func() {
			Eventually(func() *metav1.Time {
				pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}

				Expect(k8sClient.Get(context.TODO(), pprNamespacedName, ppr)).To(Succeed())
				return ppr.Status.TimeAssumedRebooted

			}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
		})

		newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
		It("Verify that node backup annotation matches the node", func() {
			pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}

			Expect(k8sClient.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
			Expect(newPpr.Status.NodeBackup).ToNot(BeNil(), "node backup should exist")
			nodeToRestore := newPpr.Status.NodeBackup

			node = &v1.Node{}
			Expect(k8sClient.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())

			//todo why do we need the following 2 lines? this might be a bug
			nodeToRestore.TypeMeta.Kind = "Node"
			nodeToRestore.TypeMeta.APIVersion = "v1"
			Expect(nodeToRestore).To(Equal(node))
		})

		It("Verify that finalizer was added", func() {
			Expect(controllerutil.ContainsFinalizer(newPpr, controllers.PPRFinalizer)).Should(BeTrue(), "finalizer should be added")
		})

		It("Verify that watchdog is not receiving food", func() {
			currentLastFoodTime := dummyDog.LastFoodTime()
			Consistently(func() time.Time {
				return dummyDog.LastFoodTime()
			}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Equal(currentLastFoodTime))
		})

		// this triggers a reconcile! It might cause invalid test results...
		//now := time.Now()
		//It("Update ppr time to accelerate the progress", func() {
		//	safeTimeToAssumeNodeRebooted := 90 * time.Second
		//	oldTime := now.Add(-safeTimeToAssumeNodeRebooted).Add(-time.Minute)
		//	oldTimeConverted := metav1.NewTime(oldTime)
		//	ppr.Status.TimeAssumedRebooted = &oldTimeConverted
		//	Expect(k8sClient.Status().Update(context.TODO(), ppr)).To(Succeed())
		//})

		It("Verify that node has been deleted and restored", func() {
			Eventually(func() time.Time {
				err := k8sClient.Get(context.TODO(), unhealthyNodeNamespacedName, node)
				if err != nil {
					return node.GetCreationTimestamp().Time
				}
				return beforePPR
			}, 100*time.Second, 250*time.Millisecond).Should(BeTemporally(">", beforePPR))
		})

		It("Verify that node is not marked as unschedulable", func() {
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), unhealthyNodeNamespacedName, node)
				if err != nil {
					return true
				}
				return node.Spec.Unschedulable
			}, 95*time.Second, 250*time.Millisecond).Should(BeFalse())
		})

		It("Verify that finalizer exists until node updates status", func() {
			Consistently(func() bool {
				pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}
				newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
				Expect(k8sClient.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
				return controllerutil.ContainsFinalizer(newPpr, controllers.PPRFinalizer)
			}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		It("Update node's last hearbeat time", func() {
			//we simulate kubelet coming up, this is required to remove the finalizer
			node.Status.Conditions = make([]v1.NodeCondition, 1)
			node.Status.Conditions[0].Status = v1.ConditionTrue
			node.Status.Conditions[0].Type = v1.NodeReady
			Expect(k8sClient.Status().Update(context.Background(), node)).To(Succeed())
		})

		It("Verify that finalizer was removed and PPR can be deleted", func() {
			Eventually(func() bool {
				pprNamespacedName := client.ObjectKey{Name: unhealthyNodeName, Namespace: pprNamespace}
				newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
				Expect(k8sClient.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
				return controllerutil.ContainsFinalizer(newPpr, controllers.PPRFinalizer)
			}, 10*time.Second, 250*time.Millisecond).Should(BeFalse())
		})

	})

	Context("Unhealthy node without api-server access", func() {

		// this is not a controller test anymore... it's testing peers. But keep it here for now...

		It("Simulate api-server failure", func() {
			k8sClient.ShouldSimulateFailure = true
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

func updateIsRebootCapable(rebootCapableLabelValue string) {
	unhealthyNodeKey := types.NamespacedName{
		Name: unhealthyNodeName,
	}
	unhealthyNode := &v1.Node{}
	Expect(k8sClient.Client.Get(context.Background(), unhealthyNodeKey, unhealthyNode)).To(Succeed())
	if unhealthyNode.Annotations == nil {
		unhealthyNode.Annotations = map[string]string{}
	}
	if rebootCapableLabelValue != "" {
		unhealthyNode.Annotations[isRebootCapableAnnotation] = rebootCapableLabelValue
	}

	Expect(k8sClient.Update(context.Background(), unhealthyNode)).To(Succeed())
}
