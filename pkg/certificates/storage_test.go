package certificates

import (
	"bytes"
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("Certificates", func() {

	Describe("Storage", func() {

		Describe("Secret", func() {

			It("should create and get certificates via Secret", func() {

				caData := "myCA"
				certData := "myCert"
				keyData := "myKey"

				toBuffer := func(data string) *bytes.Buffer {
					b := &bytes.Buffer{}
					b.WriteString(data)
					return b
				}

				store := NewSecretCertStorage(k8sClient, ctrl.Log.WithName("TestSecretCertStore"), "default")
				Expect(store.StoreCerts(toBuffer(caData), toBuffer(certData), toBuffer(keyData))).ToNot(HaveOccurred())

				caBuf, certBuf, keyBuf, err := store.GetCerts()
				Expect(err).ToNot(HaveOccurred())
				Expect(caBuf.String()).To(Equal(caData), "caData doesn't equal")
				Expect(certBuf.String()).To(Equal(certData), "certData doesn't equal")
				Expect(keyBuf.String()).To(Equal(keyData), "keyData doesn't equal")

			})

		})

		It("should fail with timeout when secret informer cache is not synced yet", func() {
			// Use a very short timeout to reproduce the issue where the cached
			// client's informer hasn't synced yet. In production, many secrets
			// cause the informer's initial List to take longer than apiTimeout.
			// Here we use a 1ms timeout: even with few secrets, the informer
			// needs at least one HTTP round trip to sync, which exceeds 1ms.
			originalTimeout := apiTimeout
			apiTimeout = 1 * time.Millisecond
			defer func() { apiTimeout = originalTimeout }()

			testNs := "test-informer-sync"

			// Use a direct (non-cached) client for test setup so we don't
			// interfere with the fresh manager's cache below.
			directClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
			Expect(err).ToNot(HaveOccurred())

			ctx := context.Background()

			ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNs}}
			Expect(directClient.Create(ctx, ns)).To(Succeed())
			defer func() { _ = directClient.Delete(ctx, ns) }()

			// Create the certificate secret that GetCerts will look for.
			certSecret := &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNs,
					Name:      secretName,
				},
				Data: map[string][]byte{
					caPemKey:   []byte("test-ca"),
					certPemKey: []byte("test-cert"),
					keyPemKey:  []byte("test-key"),
				},
			}
			Expect(directClient.Create(ctx, certSecret)).To(Succeed())
			defer func() { _ = directClient.Delete(ctx, certSecret) }()

			// Create additional secrets to make the informer's initial List
			// slower, simulating a cluster with many secrets.
			numSecrets := 100
			for i := 0; i < numSecrets; i++ {
				s := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: testNs,
						Name:      fmt.Sprintf("dummy-secret-%d", i),
					},
					Data: map[string][]byte{"data": []byte("dummy")},
				}
				Expect(directClient.Create(ctx, s)).To(Succeed())
			}
			defer func() {
				for i := 0; i < numSecrets; i++ {
					s := &v1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: testNs,
							Name:      fmt.Sprintf("dummy-secret-%d", i),
						},
					}
					_ = directClient.Delete(ctx, s)
				}
			}()

			// Create a new manager with a completely fresh, unsynced cache.
			// This simulates the state at startup before the informer has
			// listed all secrets.
			newMgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme:  scheme.Scheme,
				Metrics: metricsServer.Options{BindAddress: "0"},
			})
			Expect(err).ToNot(HaveOccurred())

			mgrCtx, mgrCancel := context.WithCancel(ctx)
			defer mgrCancel()
			go func() {
				defer GinkgoRecover()
				_ = newMgr.Start(mgrCtx)
			}()

			// Wait for the manager to start (cache is running), but the
			// Secret informer does not exist yet — it is created on-demand
			// when the cached client's Get is first called for Secrets.
			<-newMgr.Elected()

			// Call GetCerts now that the cache is started.
			// The cached client's Get blocks until the informer completes its
			// initial List. With the short apiTimeout, the context deadline is
			// exceeded before the List finishes — reproducing the production
			// failure seen when many secrets exist in the cluster.
			store := NewSecretCertStorage(newMgr.GetClient(), ctrl.Log.WithName("TestUnsyncedCache"), testNs)
			_, _, _, err = store.GetCerts()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed waiting for *v1.Secret Informer to sync"))
		})

	})
})
