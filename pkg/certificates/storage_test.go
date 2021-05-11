package certificates

import (
	"bytes"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	ctrl "sigs.k8s.io/controller-runtime"
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
	})
})
