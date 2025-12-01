package e2e

import (
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

// needs to match CI config!
const operatorInstalledNamespaceEnvVar = "OPERATOR_NS"
const defaultTestNamespace = "self-node-remediation"

var (
	cfg          *rest.Config
	k8sClient    client.Client
	k8sClientSet *kubernetes.Clientset
	logger       logr.Logger

	// The namespace the operator is installed in (needs to match CI config)
	testNamespace string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func(ctx SpecContext) {

	// don't limit log length
	format.MaxLength = 0

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	logger = logf.Log

	testNamespace = os.Getenv(operatorInstalledNamespaceEnvVar)
	if testNamespace == "" {
		logger.Info("Env var for operator's namespace not set, thus it uses default namespace as test namespace",
			"envVar", operatorInstalledNamespaceEnvVar,
			"namespace", defaultTestNamespace,
		)
		testNamespace = defaultTestNamespace
	} else {
		logger.Info("Env var for operator's namespace was set, thus it uses provided namespace as test namespace",
			"envVar", operatorInstalledNamespaceEnvVar,
			"namespace", testNamespace,
		)
	}

	err := v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	cfg, err = config.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	k8sClient, err = client.New(cfg, client.Options{})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).ToNot(BeNil())

	k8sClientSet, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClientSet).ToNot(BeNil())

}, NodeTimeout(5*time.Minute))
