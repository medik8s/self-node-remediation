package peerhealth

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
)

var _ = Describe("Checking health using grpc client and server", func() {

	var phServer *Server
	var cancel context.CancelFunc
	var phClient *Client

	BeforeEach(func() {

		By("Creating certificates")
		caPem, certPem, keyPem, err := certificates.CreateCerts()
		Expect(err).ToNot(HaveOccurred())

		By("Creating test memory cert storage")
		certReader := &certificates.MemoryCertStorage{
			CaPem:   caPem,
			CertPem: certPem,
			KeyPem:  keyPem,
		}

		By("Creating server")
		phServer, err = NewServer(pprr, cfg, ctrl.Log.WithName("peerhealth test").WithName("phServer"), 9000, certReader)
		Expect(err).ToNot(HaveOccurred())

		By("Starting server")
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		go func() {
			phServer.Start(ctx)
		}()

		By("Creating client credentials")
		clientCreds, err := certificates.GetClientCredentialsFromCerts(certReader)
		Expect(err).ToNot(HaveOccurred())

		By("Creating client")
		phClient, err = NewClient("127.0.0.1:9000", 5*time.Second, ctrl.Log.WithName("peerhealth test").WithName("phClient"), clientCreds)
		Expect(err).ToNot(HaveOccurred())

	})

	AfterEach(func() {
		cancel()
		phClient.Close()
	})

	Describe("for a healthy node", func() {
		It("should return healthy", func() {

			By("calling isHealthy")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer (cancel)()
			resp, err := phClient.IsHealthy(ctx, &HealthRequest{
				NodeName: nodeName,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(api.HealthCheckResponseCode(resp.Status)).To(Equal(api.Healthy))

		})
	})

	Describe("for an unhealthy node", func() {

		BeforeEach(func() {
			By("creating a PPR")
			snr := &v1alpha1.SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeName,
					Namespace: "default",
				},
			}
			err := k8sClient.Create(context.Background(), snr)
			Expect(err).ToNot(HaveOccurred())

			// wait until reconciled
			Eventually(func() bool {
				return pprr.GetLastSeenSnrNamespace() != ""
			}, 5*time.Second, 250*time.Millisecond).Should(BeTrue(), "SNR not reconciled")
		})

		It("should return unhealthy", func() {

			By("calling isHealthy")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer (cancel)()
			resp, err := phClient.IsHealthy(ctx, &HealthRequest{
				NodeName: nodeName,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(api.HealthCheckResponseCode(resp.Status)).To(Equal(api.Unhealthy))

		})

	})

})
