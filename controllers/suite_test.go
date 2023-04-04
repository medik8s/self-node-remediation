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

package controllers_test

import (
	"context"
	"errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/apicheck"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var k8sClient *K8sClientWrapper
var testEnv *envtest.Environment
var dummyDog watchdog.Watchdog
var certReader certificates.CertStorageReader
var unhealthyNode = &v1.Node{}
var peerNode = &v1.Node{}
var cancelFunc context.CancelFunc

var unhealthyNodeNamespacedName = client.ObjectKey{
	Name:      unhealthyNodeName,
	Namespace: "",
}
var peerNodeNamespacedName = client.ObjectKey{
	Name:      peerNodeName,
	Namespace: "",
}

const (
	peerUpdateInterval = 30 * time.Second
	apiCheckInterval   = 1 * time.Second
	maxErrorThreshold  = 1

	namespace         = "self-node-remediation"
	unhealthyNodeName = "node1"
	peerNodeName      = "node2"
)

type K8sClientWrapper struct {
	client.Client
	Reader                  client.Reader
	ShouldSimulateFailure   bool
	ShouldSimulateVaFailure bool
	VaFailureMessage        string
}

func (kcw *K8sClientWrapper) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if kcw.ShouldSimulateFailure {
		return errors.New("simulation of client error")
	} else if kcw.ShouldSimulateVaFailure {
		if _, ok := list.(*storagev1.VolumeAttachmentList); ok {
			return errors.New(kcw.VaFailureMessage)
		}
	}
	return kcw.Client.List(ctx, list, opts...)
}

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
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

	k8sClient = &K8sClientWrapper{
		k8sManager.GetClient(),
		k8sManager.GetAPIReader(),
		false,
		false,
		"simulation of client error for VA",
	}
	Expect(k8sClient).ToNot(BeNil())

	nsToCreate := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}

	Expect(k8sClient.Create(context.Background(), nsToCreate)).To(Succeed())

	err = (&controllers.SelfNodeRemediationConfigReconciler{
		Client:            k8sManager.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("self-node-remediation-config-controller"),
		InstallFileFolder: "../install/",
		Scheme:            scheme.Scheme,
		Namespace:         namespace,
	}).SetupWithManager(k8sManager)

	// peers need their own node on start
	unhealthyNode = getNode(unhealthyNodeName)
	Expect(k8sClient.Create(context.Background(), unhealthyNode)).To(Succeed(), "failed to create unhealthy node")

	peerNode = getNode(peerNodeName)
	Expect(k8sClient.Create(context.Background(), peerNode)).To(Succeed(), "failed to create peer node")

	dummyDog, err = watchdog.NewFake(true)
	Expect(err).ToNot(HaveOccurred())
	err = k8sManager.Add(dummyDog)
	Expect(err).ToNot(HaveOccurred())

	peerApiServerTimeout := 5 * time.Second
	peers := peers.New(unhealthyNodeName, peerUpdateInterval, k8sClient, ctrl.Log.WithName("peers"), peerApiServerTimeout)
	err = k8sManager.Add(peers)
	Expect(err).ToNot(HaveOccurred())

	certReader = certificates.NewSecretCertStorage(k8sClient, ctrl.Log.WithName("SecretCertStorage"), namespace)
	timeToAssumeNodeRebooted := time.Duration(maxErrorThreshold) * apiCheckInterval
	timeToAssumeNodeRebooted += dummyDog.GetTimeout()
	timeToAssumeNodeRebooted += 5 * time.Second
	rebooter := reboot.NewWatchdogRebooter(dummyDog, ctrl.Log.WithName("rebooter"), &mockCalculator{mockTimeToAssumeNodeRebooted: timeToAssumeNodeRebooted})
	apiConnectivityCheckConfig := &apicheck.ApiConnectivityCheckConfig{
		Log:                ctrl.Log.WithName("api-check"),
		MyNodeName:         unhealthyNodeName,
		CheckInterval:      apiCheckInterval,
		MaxErrorsThreshold: maxErrorThreshold,
		Peers:              peers,
		Rebooter:           rebooter,
		Cfg:                cfg,
		CertReader:         certReader,
	}
	apiCheck := apicheck.New(apiConnectivityCheckConfig, nil)
	err = k8sManager.Add(apiCheck)
	Expect(err).ToNot(HaveOccurred())

	restoreNodeAfter := 5 * time.Second

	// reconciler for unhealthy node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:           k8sClient,
		Log:              ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("unhealthy node"),
		Rebooter:         rebooter,
		MyNodeName:       unhealthyNodeName,
		RestoreNodeAfter: restoreNodeAfter,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	// reconciler for peer node
	err = (&controllers.SelfNodeRemediationReconciler{
		Client:           k8sClient,
		Log:              ctrl.Log.WithName("controllers").WithName("self-node-remediation-controller").WithName("peer node"),
		MyNodeName:       peerNodeName,
		Rebooter:         rebooter,
		RestoreNodeAfter: restoreNodeAfter,
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

func getNode(name string) *v1.Node {
	node := &v1.Node{}
	node.Name = name
	node.Labels = make(map[string]string)
	node.Labels["kubernetes.io/hostname"] = unhealthyNodeName

	return node
}

var _ = AfterSuite(func() {
	cancelFunc()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

})

type mockCalculator struct {
	mockTimeToAssumeNodeRebooted time.Duration
}

func (m *mockCalculator) GetTimeToAssumeNodeRebooted() time.Duration {
	return m.mockTimeToAssumeNodeRebooted
}

//goland:noinspection GoUnusedParameter
func (m *mockCalculator) Start(ctx context.Context) error {
	return nil
}
