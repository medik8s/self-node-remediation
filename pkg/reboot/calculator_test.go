package reboot_test

import (
	"context"
	"strconv"
	"time"

	"github.com/medik8s/common/pkg/labels"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

var _ = Describe("Calculator tests", func() {

	var snrConfig *v1alpha1.SelfNodeRemediationConfig
	var watchdogTimeoutSeconds int
	var unhealthyNode *v1.Node
	var nrOfPeers int
	var expectedRebootDurationSeconds int

	BeforeEach(func() {
		// just defaults, override values in tests
		snrConfig = &v1alpha1.SelfNodeRemediationConfig{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      v1alpha1.ConfigCRName,
			},
			Spec: v1alpha1.SelfNodeRemediationConfigSpec{},
		}
	})

	JustBeforeEach(func() {
		Expect(k8sClient.Create(context.Background(), snrConfig)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(context.Background(), snrConfig)).To(Succeed())
			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), &v1alpha1.SelfNodeRemediationConfig{})
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, "5s", "1s").Should(Succeed())
			calculator.SetConfig(nil)
		})

		unhealthyNode = getNode("unhealthy-node")
		unhealthyNode.Annotations = map[string]string{
			utils.WatchdogTimeoutSecondsAnnotation: strconv.Itoa(watchdogTimeoutSeconds),
		}
		Expect(k8sClient.Create(context.Background(), unhealthyNode)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(context.Background(), unhealthyNode)).To(Succeed())
			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(unhealthyNode), &v1.Node{})
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, "5s", "1s").Should(Succeed())
		})

		for i := 0; i < nrOfPeers; i++ {
			peer := getNode("peer-node-" + strconv.Itoa(i))
			peer.Labels[labels.WorkerRole] = ""
			Expect(k8sClient.Create(context.Background(), peer)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(context.Background(), peer)).To(Succeed())
				Eventually(func(g Gomega) {
					err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(peer), &v1.Node{})
					g.Expect(errors.IsNotFound(err)).To(BeTrue())
				}, "5s", "1s").Should(Succeed())
			})
		}
	})

	Context("with default SNRConfig, 2 peers, and 10s watchdog timeout", func() {
		BeforeEach(func() {
			watchdogTimeoutSeconds = 10
			nrOfPeers = 2
			// 3 * (15 + 5) (API server)
			// + 30 (MaxTimeForNoPeersResponse)
			// + 10 (Watchdog)
			// + 30
			expectedRebootDurationSeconds = 130
		})
		It("GetRebootTime should return correct value", func() {
			Eventually(func() (time.Duration, error) {
				return calculator.GetRebootDuration(context.Background(), unhealthyNode)
			}, "5s", "200ms").Should(Equal(time.Duration(expectedRebootDurationSeconds) * time.Second))
		})
	})

	Context("with modified SNRConfig, 20 peers, and 25s watchdog timeout", func() {
		BeforeEach(func() {
			// modify all values used by calculator
			snrConfig.Spec.ApiCheckInterval = &metav1.Duration{Duration: 25 * time.Second}
			snrConfig.Spec.ApiServerTimeout = &metav1.Duration{Duration: 7 * time.Second}
			snrConfig.Spec.MaxApiErrorThreshold = 4

			snrConfig.Spec.PeerDialTimeout = &metav1.Duration{Duration: 11 * time.Second}
			snrConfig.Spec.PeerRequestTimeout = &metav1.Duration{Duration: 13 * time.Second}

			watchdogTimeoutSeconds = 25
			nrOfPeers = 20 // 7 batches

			// 4 * (25 + 7) = 128 (API server)
			// + 7 * (11 + 13) = 168 (Peers)
			// + 25 (Watchdog)
			// + 30
			expectedRebootDurationSeconds = 351
		})
		It("GetRebootTime should return correct value", func() {
			Eventually(func() (time.Duration, error) {
				return calculator.GetRebootDuration(context.Background(), unhealthyNode)
			}, "5s", "200ms").Should(Equal(time.Duration(expectedRebootDurationSeconds) * time.Second))
		})
	})
})

func getNode(name string) *v1.Node {
	node := &v1.Node{}
	node.Name = name
	node.Labels = make(map[string]string)
	node.Labels["kubernetes.io/hostname"] = name
	return node
}
