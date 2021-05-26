package controllers

import (
	"context"
	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"time"
)

const (
	nodeName     string = "node1"
	pprNamespace string = "default"
)

var _ = Describe("ppr Controller", func() {

	Context("Unhealthy node with api-server access", func() {
		logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		It("Disable api-server failure simulation", func() {
			shouldReboot = false
			apiReaderWrapper.ShouldSimulateFailure = false
		})

		It("Create a new node", func() {
			node1 := &v1.Node{}
			node1.Name = nodeName
			Expect(k8sClient.Create(context.Background(), node1)).To(Succeed(), "failed to create node")
			Expect(os.Setenv(nodeNameEnvVar, node1.Name)).To(Succeed(), "failed to set env variable of the node name")
		})

		nodeNamespacedName := client.ObjectKey{
			Name:      nodeName,
			Namespace: "",
		}

		It("Check the node exists", func() {
			node1 := &v1.Node{}
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), nodeNamespacedName, node1)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(node1.Name).To(Equal(nodeName))
			Expect(node1.CreationTimestamp).ToNot(BeZero())
		})

		It("Create ppr for node1", func() {
			ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
			ppr.Name = nodeName
			ppr.Namespace = pprNamespace
			Expect(k8sClient.Create(context.TODO(), ppr)).To(Succeed(), "failed to create ppr CR")
		})

		node := &v1.Node{}

		It("Verify that node was marked as unschedulable ", func() {
			Eventually(func() bool {
				node = &v1.Node{}
				Expect(k8sClient.Get(context.TODO(), nodeNamespacedName, node)).To(Succeed())

				return node.Spec.Unschedulable

			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
		})

		It("Add unschedulable taint to node to simulate node controller", func() {
			node.Spec.Taints = append(node.Spec.Taints, *NodeUnschedulableTaint)
			Expect(k8sClient.Update(context.TODO(), node)).To(Succeed())
		})

		ppr := &poisonpillv1alpha1.PoisonPillRemediation{}
		It("Verify that time has been added to annotation", func() {
			Eventually(func() *metav1.Time {
				pprNamespacedName := client.ObjectKey{Name: nodeName, Namespace: pprNamespace}

				Expect(k8sClient.Get(context.TODO(), pprNamespacedName, ppr)).To(Succeed())
				return ppr.Status.TimeAssumedRebooted

				//give some time to the controller to update the time in the annotation

			}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
		})

		It("Verify that node backup annotation matches the node", func() {
			pprNamespacedName := client.ObjectKey{Name: nodeName, Namespace: pprNamespace}
			time.Sleep(time.Second * 10)
			newPpr := &poisonpillv1alpha1.PoisonPillRemediation{}
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

		It("Verify that watchdog is not receiving food", func() {
			currentLastFoodTime := dummyDog.LastFoodTime()
			Consistently(func() time.Time {
				return dummyDog.LastFoodTime()
			}, 5*reconcileInterval, 1*time.Second).Should(Equal(currentLastFoodTime))
		})

		now := time.Now()
		It("Update ppr time to accelerate the progress", func() {
			safeTimeToAssumeNodeRebooted := 90 * time.Second
			oldTime := now.Add(-safeTimeToAssumeNodeRebooted).Add(-time.Minute)
			oldTimeConverted := metav1.NewTime(oldTime)
			ppr.Status.TimeAssumedRebooted = &oldTimeConverted
			Expect(k8sClient.Status().Update(context.TODO(), ppr)).To(Succeed())
		})

		It("Verify that node has been deleted and restored", func() {
			// in real world scenario, other nodes will take care for the rest of the test but
			// in this test, we trick the node to recover itself after we already verified it
			// tried to reboot

			// stop rebooting yourself, we want you to recover the node
			shouldReboot = false

			node = &v1.Node{}

			Eventually(func() error {
				return k8sClient.Get(context.TODO(), nodeNamespacedName, node)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(node.CreationTimestamp.After(now)).To(BeTrue())
		})

		It("Verify that node is not marked as unschedulable", func() {
			Eventually(func() bool {
				node = &v1.Node{}
				Expect(k8sClient.Get(context.TODO(), nodeNamespacedName, node)).To(Succeed())
				return node.Spec.Unschedulable
			}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
		})

	})

	Context("Unhealthy node without api-server access", func() {
		logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		node1 := &v1.Node{}
		nodeNamespacedName := client.ObjectKey{
			Name:      nodeName,
			Namespace: "",
		}

		It("Check the node exists", func() {
			Eventually(func() error {
				return k8sClient.Get(context.TODO(), nodeNamespacedName, node1)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(node1.Name).To(Equal(nodeName))
		})

		It("Simulate api-server failure", func() {
			apiReaderWrapper.ShouldSimulateFailure = true
		})

		It("Sleep", func() {
			Consistently(func() bool {
				return true
			}, (maxFailuresThreshold+2)*reconcileInterval, 1*time.Second).Should(BeTrue())
		})

		It("Verify that watchdog is not receiving food", func() {
			currentLastFoodTime := dummyDog.LastFoodTime()
			Consistently(func() time.Time {
				return dummyDog.LastFoodTime()
			}, 5*reconcileInterval, 1*time.Second).Should(Equal(currentLastFoodTime))
		})
	})
})
