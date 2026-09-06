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

				store := NewSecretCertStorage(k8sClient, k8sCache, ctrl.Log.WithName("TestSecretCertStore"), "default")
				Expect(store.StoreCerts(toBuffer(caData), toBuffer(certData), toBuffer(keyData))).ToNot(HaveOccurred())

				caBuf, certBuf, keyBuf, err := store.GetCerts()
				Expect(err).ToNot(HaveOccurred())
				Expect(caBuf.String()).To(Equal(caData), "caData doesn't equal")
				Expect(certBuf.String()).To(Equal(certData), "certData doesn't equal")
				Expect(keyBuf.String()).To(Equal(keyData), "keyData doesn't equal")

			})

		})

		It("should succeed even when secret informer cache is not synced yet", func() {
			// Regression test: previously GetCerts would fail with
			// "failed waiting for *v1.Secret Informer to sync" because
			// the cached client's Get blocks until the informer completes
			// its initial List, which in clusters with many secrets can
			// exceed the apiTimeout. The fix waits for the informer to
			// sync separately via cache.GetInformer before calling Get.
			//
			// We use a very short apiTimeout to prove that the informer
			// sync is handled independently: even with a 1ms Get timeout,
			// GetCerts succeeds because GetInformer waits for sync first,
			// after which Get reads from the local cache instantly.
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

			// Call GetCerts with a fresh cache and a 1ms apiTimeout.
			// Without the fix this would fail with:
			//   "failed waiting for *v1.Secret Informer to sync"
			// With the fix, GetInformer waits for the sync to complete,
			// then Get reads from the local cache and succeeds.
			store := NewSecretCertStorage(newMgr.GetClient(), newMgr.GetCache(), ctrl.Log.WithName("TestUnsyncedCache"), testNs)
			caBuf, certBuf, keyBuf, err := store.GetCerts()
			Expect(err).ToNot(HaveOccurred())
			Expect(caBuf.String()).To(Equal("test-ca"))
			Expect(certBuf.String()).To(Equal("test-cert"))
			Expect(keyBuf.String()).To(Equal("test-key"))
		})

	})
})

var _ = Describe("Certificate Namespace Scoping", func() {
	It("should always create secret in operator's namespace", func() {
		// This test validates that SecretCertStorage enforces namespace scoping.
		// SNR only needs access to secrets in the operator's deployment namespace,
		// not cluster-wide (principle of least privilege). This test ensures the secret is created in
		// the correct namespace as specified during SecretCertStorage construction.

		operatorNs := "snr-operator-namespace"
		otherNs := "some-other-namespace"

		ctx := context.Background()
		directClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).ToNot(HaveOccurred())

		// Create test namespaces
		opNs := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: operatorNs}}
		Expect(directClient.Create(ctx, opNs)).To(Succeed())
		defer func() { _ = directClient.Delete(ctx, opNs) }()

		otherNsObj := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNs}}
		Expect(directClient.Create(ctx, otherNsObj)).To(Succeed())
		defer func() { _ = directClient.Delete(ctx, otherNsObj) }()

		// Create SecretCertStorage with operator namespace
		store := NewSecretCertStorage(k8sClient, k8sCache, ctrl.Log.WithName("TestNamespaceScoping"), operatorNs)

		toBuffer := func(data string) *bytes.Buffer {
			b := &bytes.Buffer{}
			b.WriteString(data)
			return b
		}

		// Store certs - should create in operator namespace only
		Expect(store.StoreCerts(toBuffer("ca"), toBuffer("cert"), toBuffer("key"))).ToNot(HaveOccurred())

		// Verify secret exists in operator namespace
		secretInOperatorNs := &v1.Secret{}
		err = directClient.Get(ctx, client.ObjectKey{
			Namespace: operatorNs,
			Name:      secretName,
		}, secretInOperatorNs)
		Expect(err).ToNot(HaveOccurred(), "Secret should exist in operator namespace")
		Expect(secretInOperatorNs.Namespace).To(Equal(operatorNs))

		// Verify secret does NOT exist in other namespace
		secretInOtherNs := &v1.Secret{}
		err = directClient.Get(ctx, client.ObjectKey{
			Namespace: otherNs,
			Name:      secretName,
		}, secretInOtherNs)
		Expect(err).To(HaveOccurred(), "Secret should NOT exist in other namespace")
		Expect(err.Error()).To(ContainSubstring("not found"))

		// Cleanup
		_ = directClient.Delete(ctx, secretInOperatorNs)
	})
})
