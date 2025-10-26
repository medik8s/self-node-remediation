package certificates

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestCertificates(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Certificates Suite")
}

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var cancelFunc context.CancelFunc
var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		BinaryAssetsDirectory: "../../testbin/bin",
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsServer.Options{BindAddress: "0"},
	})
	Expect(err).ToNot(HaveOccurred())
	var ctx context.Context
	ctx, cancelFunc = context.WithCancel(ctrl.SetupSignalHandler())
	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

	k8sClient = k8sManager.GetClient()
	Expect(k8sClient).ToNot(BeNil())

})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancelFunc()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
