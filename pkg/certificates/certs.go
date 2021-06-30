package certificates

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

// this used in the server cert, and as servername override in the client, so the IP check always succeeds no matter
// what the real IP of the server pod is
// TODO reconsider a better to deal with the IP check...?
var fixedCertIP = net.IPv4(192, 0, 2, 1)

func createCertTemplate(isCa bool) *x509.Certificate {
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(12345), // TODO randomize?
		Subject: pkix.Name{
			Organization: []string{"medik8s"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  isCa,
		BasicConstraintsValid: isCa,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	if isCa {
		cert.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign
	} else {
		cert.IPAddresses = []net.IP{fixedCertIP}
	}
	return cert
}

func createPrivKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 4096)
}

func selfSign(cert *x509.Certificate, privKey *rsa.PrivateKey) ([]byte, error) {
	return sign(cert, cert, &privKey.PublicKey, privKey)
}

func sign(cert *x509.Certificate, ca *x509.Certificate, certPubKey *rsa.PublicKey, caPrivKey *rsa.PrivateKey) ([]byte, error) {
	return x509.CreateCertificate(rand.Reader, cert, ca, certPubKey, caPrivKey)
}

func certToPEM(cert []byte) (*bytes.Buffer, error) {
	return toPem(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert,
	})
}

func privKeyToPEM(privKey *rsa.PrivateKey) (*bytes.Buffer, error) {
	return toPem(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
}

func toPem(block *pem.Block) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	err := pem.Encode(buf, block)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func CreateCerts() (caCertPem, certPem, keyPem *bytes.Buffer, retErr error) {
	// Create self signed CA certificate
	caCert := createCertTemplate(true)
	caKey, err := createPrivKey()
	if err != nil {
		return nil, nil, nil, err
	}
	caSignedBytes, err := selfSign(caCert, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	caCertPem, err = certToPEM(caSignedBytes)
	if err != nil {
		return nil, nil, nil, err
	}

	// Create server / client certificate
	cert := createCertTemplate(false)
	key, err := createPrivKey()
	if err != nil {
		return nil, nil, nil, err
	}
	certSignedBytes, err := sign(cert, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	certPem, err = certToPEM(certSignedBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPem, err = privKeyToPEM(key)
	if err != nil {
		return nil, nil, nil, err
	}

	return
}
