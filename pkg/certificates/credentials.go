package certificates

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"google.golang.org/grpc/credentials"
)

const TLSMinVersion = tls.VersionTLS13

func GetServerCredentialsFromCerts(certReader CertStorageReader) (credentials.TransportCredentials, error) {

	keyPair, pool, err := PrepareCredentials(certReader)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{*keyPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   TLSMinVersion,
	}), nil
}

func GetClientCredentialsFromCerts(certReader CertStorageReader) (credentials.TransportCredentials, error) {

	keyPair, pool, err := PrepareCredentials(certReader)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{*keyPair},
		RootCAs:      pool,
		ServerName:   fixedCertIP.String(),
		MinVersion:   TLSMinVersion,
	}), nil
}

func PrepareCredentials(certReader CertStorageReader) (*tls.Certificate, *x509.CertPool, error) {
	caPem, certPem, keyPem, err := certReader.GetCerts()
	if err != nil {
		return nil, nil, err
	}

	keyPair, err := tls.X509KeyPair(certPem.Bytes(), keyPem.Bytes())
	if err != nil {
		return nil, nil, err
	}
	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(caPem.Bytes()) {
		return nil, nil, fmt.Errorf("credentials: failed to append ca cert")
	}
	return &keyPair, cp, nil
}
