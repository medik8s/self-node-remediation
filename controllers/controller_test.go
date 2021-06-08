package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	nodeName     string = "node1"
	pprNamespace string = "default"
)

var _ = Describe("ppr Controller", func() {

	nodeNamespacedName := client.ObjectKey{
		Name:      nodeName,
		Namespace: "",
	}

	Context("Unhealthy node with api-server access", func() {

		It("Disable api-server failure", func() {
			k8sClient.ShouldSimulateFailure = false
		})

		It("Check the node exists", func() {
			node1 := &v1.Node{}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), nodeNamespacedName, node1)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(node1.Name).To(Equal(nodeName))
			Expect(node1.CreationTimestamp).ToNot(BeZero())
		})

		beforePPR := time.Now()

		It("Create ppr for node1", func() {
			ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
			ppr.Name = nodeName
			ppr.Namespace = pprNamespace
			Expect(k8sClient.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
		})

		node := &v1.Node{}
		It("Verify that node was marked as unschedulable ", func() {
			Eventually(func() bool {
				Expect(k8sClient.Get(context.TODO(), nodeNamespacedName, node)).To(Succeed())
				return node.Spec.Unschedulable
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		It("Add unschedulable taint to node to simulate node controller", func() {
			node.Spec.Taints = append(node.Spec.Taints, *NodeUnschedulableTaint)
			Expect(k8sClient.Update(context.TODO(), node)).To(Succeed())
		})

		ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
		It("Verify that time has been added to PPR status", func() {
			Eventually(func() *metav1.Time {
				pprNamespacedName := client.ObjectKey{Name: nodeName, Namespace: pprNamespace}

				Expect(k8sClient.Get(context.TODO(), pprNamespacedName, ppr)).To(Succeed())
				return ppr.Status.TimeAssumedRebooted

			}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
		})

		newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
		It("Verify that node backup annotation matches the node", func() {
			pprNamespacedName := client.ObjectKey{Name: nodeName, Namespace: pprNamespace}

			Expect(k8sClient.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
			Expect(newPpr.Status.NodeBackup).ToNot(BeNil(), "node backup should exist")
			nodeToRestore := newPpr.Status.NodeBackup

			node = &v1.Node{}
			Expect(k8sClient.Get(context.TODO(), nodeNamespacedName, node)).To(Succeed())

			//todo why do we need the following 2 lines? this might be a bug
			nodeToRestore.TypeMeta.Kind = "Node"
			nodeToRestore.TypeMeta.APIVersion = "v1"
			Expect(nodeToRestore).To(Equal(node))
		})

		It("Verify that finalizer was added", func() {
			Expect(controllerutil.ContainsFinalizer(newPpr, pprFinalizer)).Should(BeTrue(), "finalizer should be added")
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
			// in real world scenario, other nodes will take care for the rest of the test but
			// in this test, we trick the node to recover itself after we already verified it
			// tried to reboot

			// stop rebooting yourself, we want you to recover the node
			//shouldReboot = false

			Eventually(func() time.Time {
				err := k8sClient.Get(context.TODO(), nodeNamespacedName, node)
				if err != nil {
					return node.GetCreationTimestamp().Time
				}
				return beforePPR
			}, 100*time.Second, 250*time.Millisecond).Should(BeTemporally(">", beforePPR))

		})

		It("Verify that node is not marked as unschedulable", func() {
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), nodeNamespacedName, node)
				if err != nil {
					return true
				}
				return node.Spec.Unschedulable
			}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
		})

		It("Verify that finalizer was removed", func() {
			Eventually(func() bool {
				pprNamespacedName := client.ObjectKey{Name: nodeName, Namespace: pprNamespace}
				newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
				Expect(k8sClient.Get(context.TODO(), pprNamespacedName, newPpr)).To(Succeed())
				return controllerutil.ContainsFinalizer(newPpr, pprFinalizer)
			}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
		})

	})

	Context("Unhealthy node without api-server access", func() {

		// this is not a controller test anymore... it's testing peers. But keep it here for now...

		It("Simulate api-server failure", func() {
			k8sClient.ShouldSimulateFailure = true
		})

		// TODO this does not happen, because there are no peers which confirm we're unhealthy...
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
