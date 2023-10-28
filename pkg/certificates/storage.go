package certificates

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CertStorageReader interface {
	GetCerts() (caPem, certPem, keyPem *bytes.Buffer, err error)
}

// for tests only
var _ CertStorageReader = &MemoryCertStorage{}

type MemoryCertStorage struct {
	CaPem, CertPem, KeyPem *bytes.Buffer
}

func (m *MemoryCertStorage) GetCerts() (caPem, certPem, keyPem *bytes.Buffer, err error) {
	return m.CaPem, m.CertPem, m.KeyPem, nil
}

const (
	secretName = "self-node-remediation-certificates"
	caPemKey   = "caPem"
	certPemKey = "certPem"
	keyPemKey  = "keyPem"

	apiTimeout = 10 * time.Second
)

var _ CertStorageReader = &SecretCertStorage{}

type SecretCertStorage struct {
	client.Client
	log       logr.Logger
	namespace string
	secret    *v1.Secret
	mutex     sync.Mutex
}

//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

func NewSecretCertStorage(c client.Client, log logr.Logger, namespace string) *SecretCertStorage {
	return &SecretCertStorage{
		Client:    c,
		log:       log,
		namespace: namespace,
		mutex:     sync.Mutex{},
	}
}

func (s *SecretCertStorage) GetCerts() (caPem, certPem, keyPem *bytes.Buffer, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.secret == nil {
		certSecret := &v1.Secret{}
		key := types.NamespacedName{
			Namespace: s.namespace,
			Name:      secretName,
		}
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		if err := s.Get(ctx, key, certSecret); err != nil {
			return nil, nil, nil, err
		}
		s.secret = certSecret
	}

	toBuffer := func(key string) *bytes.Buffer {
		b := &bytes.Buffer{}
		b.Write(s.secret.Data[key])
		return b
	}
	caPem = toBuffer(caPemKey)
	certPem = toBuffer(certPemKey)
	keyPem = toBuffer(keyPemKey)
	return
}

func (s *SecretCertStorage) StoreCerts(caPem, certPem, keyPem *bytes.Buffer) error {
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.namespace,
			Name:      secretName,
		},
		Immutable: pointer.Bool(true),
		Data:      nil,
		StringData: map[string]string{
			caPemKey:   caPem.String(),
			certPemKey: certPem.String(),
			keyPemKey:  keyPem.String(),
		},
		Type: v1.SecretTypeOpaque,
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	if err := s.Create(ctx, secret); err != nil {
		if errors.IsAlreadyExists(err) {
			s.log.Info("certificates already stored in secret")
			return nil
		}
		return err
	}

	return nil
}
