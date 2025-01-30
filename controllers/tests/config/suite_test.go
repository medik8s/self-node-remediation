/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package testconfig

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/controllers/tests/shared"
	"github.com/medik8s/self-node-remediation/pkg/apicheck"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var testEnv *envtest.Environment
var unhealthyNode, peerNode = &v1.Node{}, &v1.Node{}
var cancelFunc context.CancelFunc
var k8sClient *shared.K8sClientWrapper
var certReader certificates.CertStorageReader
var managerReconciler *controllers.SelfNodeRemediationReconciler

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SNR Config Test Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("../../..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = selfnoderemediationv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0",
	})
	Expect(err).ToNot(HaveOccurred())

	k8sClient = &shared.K8sClientWrapper{
		Client: k8sManager.GetClient(),
		Reader: k8sManager.GetAPIReader(),
	}
	Expect(k8sClient).ToNot(BeNil())

	nsToCreate := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: shared.Namespace,
		},
	}

	_ = os.Setenv("SELF_NODE_REMEDIATION_IMAGE", shared.DsDummyImageName)

	Expect(k8sClient.Create(context.Background(), nsToCreate)).To(Succeed())
	Expect(err).ToNot(HaveOccurred())
	err = (&controllers.SelfNodeRemediationConfigReconciler{
		Client:                   k8sManager.GetClient(),
		Log:                      ctrl.Log.WithName("controllers").WithName("self-node-remediation-config-controller"),
		InstallFileFolder:        "../../../install/",
		Scheme:                   scheme.Scheme,
		Namespace:                shared.Namespace,
		RebootDurationCalculator: shared.MockRebootDurationCalculator{},
	}).SetupWithManager(k8sManager)

	// peers need their own node on start
	unhealthyNode = getNode(shared.UnhealthyNodeName)
	Expect(k8sClient.Create(context.Background(), unhealthyNode)).To(Succeed(), "failed to create unhealthy node")

	peerNode = getNode(shared.PeerNodeName)
	Expect(k8sClient.Create(context.Background(), peerNode)).To(Succeed(), "failed to create peer node")

	peerApiServerTimeout := 5 * time.Second
	peers := peers.New(shared.UnhealthyNodeName, shared.PeerUpdateInterval, k8sClient, ctrl.Log.WithName("peers"), peerApiServerTimeout)
	err = k8sManager.Add(peers)
	Expect(err).ToNot(HaveOccurred())

	certReader = certificates.NewSecretCertStorage(k8sClient, ctrl.Log.WithName("SecretCertStorage"), shared.Namespace)

	apiConnectivityCheckConfig := &apicheck.ApiConnectivityCheckConfig{
		Log:                    ctrl.Log.WithName("api-check"),
		MyNodeName:             shared.UnhealthyNodeName,
		CheckInterval:          shared.ApiCheckInterval,
		MaxErrorsThreshold:     shared.MaxErrorThreshold,
		MinPeersForRemediation: shared.MinPeersForRemediation,
		Peers:                  peers,
		Cfg:                    cfg,
		CertReader:             certReader,
	}
	apiCheck := apicheck.New(apiConnectivityCheckConfig, nil)
	err = k8sManager.Add(apiCheck)
	Expect(err).ToNot(HaveOccurred())

	// reconciler for unhealthy node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:     k8sClient,
		Log:        ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("unhealthy node"),
		MyNodeName: shared.UnhealthyNodeName,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	// reconciler for peer node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:     k8sClient,
		Log:        ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("peer node"),
		MyNodeName: shared.PeerNodeName,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	// reconciler for manager on peer node
	managerReconciler = &controllers.SelfNodeRemediationReconciler{
		Client:     k8sClient,
		Log:        ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("manager node"),
		MyNodeName: shared.PeerNodeName,
	}
	err = managerReconciler.SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	var ctx context.Context
	ctx, cancelFunc = context.WithCancel(ctrl.SetupSignalHandler())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()

})

func getNode(name string) *v1.Node {
	node := &v1.Node{}
	node.Name = name
	node.Labels = make(map[string]string)
	node.Labels["kubernetes.io/hostname"] = name

	return node
}

var _ = AfterSuite(func() {
	cancelFunc()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

})
