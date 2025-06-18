package peerhealth

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

		By("creating test node")
		node := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
			},
		}
		err := k8sClient.Create(context.Background(), node)
		if !errors.IsAlreadyExists(err) {
			Expect(err).ToNot(HaveOccurred())
		}

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
		phServer, err = NewServer(k8sClient, reader, ctrl.Log.WithName("peerhealth test").WithName("phServer"), 9000, certReader)
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
			defer cancel()
			resp, err := phClient.IsHealthy(ctx, &HealthRequest{
				NodeName: nodeName,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(api.HealthCheckResponseCode(resp.Status)).To(Equal(api.Healthy))

		})
	})

	Describe("for an unhealthy node", func() {

		BeforeEach(func() {
			By("creating a SNR")
			snr := &v1alpha1.SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeName,
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{Name: "Dummy", Kind: "NodeHealthCheck", APIVersion: "Dummy", UID: "Dummy"},
					},
				},
			}
			err := k8sClient.Create(context.Background(), snr)
			Expect(err).ToNot(HaveOccurred())

		})

		It("should return unhealthy", func() {

			By("calling isHealthy")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			Eventually(func() bool {
				resp, err := phClient.IsHealthy(ctx, &HealthRequest{
					NodeName: nodeName,
				})
				return err == nil && api.HealthCheckResponseCode(resp.Status) == api.Unhealthy
			}, time.Second*5, time.Millisecond*250).Should(BeTrue())

		})

	})

	Describe("with a peer running into API timeout", func() {

		var (
			apiCallDelay       = 7 * time.Second
			peerRequestTimeout = 5 * time.Second // must be lower than apiCallDelay for this test!
		)

		BeforeEach(func() {
			reader.delay = &apiCallDelay
		})

		AfterEach(func() {
			reader.delay = nil
		})

		It("should return API error", func() {
			By("calling isHealthy")
			// The health server code has a hardcoded timeout of 3s for the API call!
			// When we add a delay of > 3s to the API call, that 3s timeout needs to be respected,
			// to not exceed the peerRequestTimeout (5s) of the client.
			ctx, cancel := context.WithTimeout(context.Background(), peerRequestTimeout)
			defer cancel()
			resp, err := phClient.IsHealthy(ctx, &HealthRequest{
				NodeName: nodeName,
			})

			// wait for having more logs from async server code
			time.Sleep(10 * time.Second)

			Expect(err).ToNot(HaveOccurred())
			Expect(api.HealthCheckResponseCode(resp.Status)).To(Equal(api.ApiError))
		})

	})

	Describe("with a peer running into API error", func() {

		BeforeEach(func() {
			reader.err = fmt.Errorf("some API error")
		})

		AfterEach(func() {
			reader.err = nil
		})

		It("should return API error", func() {
			By("calling isHealthy")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := phClient.IsHealthy(ctx, &HealthRequest{
				NodeName: nodeName,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(api.HealthCheckResponseCode(resp.Status)).To(Equal(api.ApiError))
		})
	})
})
