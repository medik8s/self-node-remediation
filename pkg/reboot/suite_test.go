package reboot_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
)

var (
	testEnv    envtest.Environment
	cancelFunc context.CancelFunc
	k8sClient  client.Client
	calculator reboot.Calculator
)

func TestReboot(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reboot Test Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				filepath.Join("..", "..", "config", "crd", "bases"),
			},
			ErrorIfPathMissing: true,
		},
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	Expect(selfnoderemediationv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsServer.Options{BindAddress: "0"},
	})
	Expect(err).ToNot(HaveOccurred())

	k8sClient = k8sManager.GetClient()
	Expect(k8sClient).ToNot(BeNil())

	calculator = reboot.NewCalculator(k8sClient, ctrl.Log.WithName("reboot-calculator"))
	_ = os.Setenv("SELF_NODE_REMEDIATION_IMAGE", shared.DsDummyImageName)

	err = (&controllers.SelfNodeRemediationConfigReconciler{
		Client:                   k8sManager.GetClient(),
		Log:                      ctrl.Log.WithName("controllers").WithName("self-node-remediation-config-controller"),
		InstallFileFolder:        "../../install/",
		Scheme:                   scheme.Scheme,
		Namespace:                "default",
		RebootDurationCalculator: calculator,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	var ctx context.Context
	ctx, cancelFunc = context.WithCancel(ctrl.SetupSignalHandler())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

})

var _ = AfterSuite(func() {
	cancelFunc()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
