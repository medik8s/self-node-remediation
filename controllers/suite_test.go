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

package controllers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/pkg/peers"
	"github.com/medik8s/poison-pill/pkg/watchdog"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

const (
	peerUpdateInterval = 1 * time.Second
)

var cfg *rest.Config
var testEnv *envtest.Environment
var dummyDog watchdog.Watchdog
var k8sClient *K8sClientWrapper

type K8sClientWrapper struct {
	client.Client
	envVarETCD      = "TEST_ASSET_ETCD"
	envVarKUBECTL   = "TEST_ASSET_KUBECTL"
)

	ShouldSimulateFailure bool
}

func (kcw *K8sClientWrapper) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if kcw.ShouldSimulateFailure {
		return errors.New("simulation of client error")
	}
	return kcw.Client.List(ctx, list, opts...)
}

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func() {
	if _, isFound := os.LookupEnv(envVarApiServer); !isFound {
		Expect(os.Setenv(envVarApiServer, "../testbin/bin/kube-apiserver")).To(Succeed())
	}
	if _, isFound := os.LookupEnv(envVarETCD); !isFound {
		Expect(os.Setenv(envVarETCD, "../testbin/bin/etcd")).To(Succeed())
	}
	if _, isFound := os.LookupEnv(envVarKUBECTL); !isFound {
		Expect(os.Setenv(envVarKUBECTL, "../testbin/bin/kubectl")).To(Succeed())
	}

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = poisonpillv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	k8sClient = &K8sClientWrapper{
		k8sManager.GetClient(),
		false,
	}
	Expect(k8sClient).ToNot(BeNil())

	err = (&PoisonPillConfigReconciler{
		Client:            k8sManager.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("poison-pill-config-controller"),
		InstallFileFolder: "../install/",
		Scheme:            scheme.Scheme,
	}).SetupWithManager(k8sManager)

	// peers need their own node on start
	node1 := &v1.Node{}
	node1.Name = nodeName
	node1.Labels = make(map[string]string)
	node1.Labels["kubernetes.io/hostname"] = nodeName
	Expect(k8sClient.Create(context.Background(), node1)).To(Succeed(), "failed to create node")
	Expect(os.Setenv(nodeNameEnvVar, node1.Name)).To(Succeed(), "failed to set env variable of the node name")

	dummyDog, err = watchdog.NewFake(ctrl.Log.WithName("fake watchdog"))
	Expect(err).ToNot(HaveOccurred())
	peers := peers.New(dummyDog, peerUpdateInterval, 1*time.Second, 1, k8sClient, ctrl.Log.WithName("peers"))
	Expect(err).ToNot(HaveOccurred())

	err = (&PoisonPillRemediationReconciler{
		Client: k8sClient,
		Log:    ctrl.Log.WithName("controllers").WithName("poison-pill-controller"),
		Peers:  peers,
		SafeTimeToAssumeNodeRebooted: 90 * time.Second,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = k8sManager.Add(dummyDog)
	Expect(err).ToNot(HaveOccurred())
	err = k8sManager.Add(peers)
	Expect(err).ToNot(HaveOccurred())

	myNodeName = "node1"

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctrl.SetupSignalHandler())
		Expect(err).ToNot(HaveOccurred())
	}()

}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

	Expect(os.Unsetenv(envVarApiServer)).To(Succeed())
	Expect(os.Unsetenv(envVarETCD)).To(Succeed())
	Expect(os.Unsetenv(envVarKUBECTL)).To(Succeed())
})
