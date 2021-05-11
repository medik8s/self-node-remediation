package certificates

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"google.golang.org/grpc/credentials"
)

func GetServerCredentialsFromCerts(certReader CertStorageReader) (credentials.TransportCredentials, error) {

	caPem, certPem, keyPem, err := certReader.GetCerts()
	if err != nil {
		return nil, err
	}

	keyPair, err := tls.X509KeyPair(certPem.Bytes(), keyPem.Bytes())
	if err != nil {
		return nil, err
	}

	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(caPem.Bytes()) {
		return nil, fmt.Errorf("credentials: failed to append ca cert")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{keyPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    cp,
	}), nil
}

func GetClientCredentialsFromCerts(certReader CertStorageReader) (credentials.TransportCredentials, error) {

	caPem, certPem, keyPem, err := certReader.GetCerts()
	if err != nil {
		return nil, err
	}

	keyPair, err := tls.X509KeyPair(certPem.Bytes(), keyPem.Bytes())
	if err != nil {
		return nil, err
	}
	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(caPem.Bytes()) {
		return nil, fmt.Errorf("credentials: failed to append ca cert")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{keyPair},
		RootCAs:      cp,
		ServerName:   fixedCertIP.String(),
	}), nil
}
