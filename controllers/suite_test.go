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

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/pkg/watchdog"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var dummyDog watchdog.Watchdog
var apiReaderWrapper ApiReaderWrapper

const (
	envVarApiServer = "TEST_ASSET_KUBE_APISERVER"
	envVarETCD      = "TEST_ASSET_ETCD"
	envVarKUBECTL   = "TEST_ASSET_KUBECTL"
)

type ApiReaderWrapper struct {
	apiReader             client.Reader
	ShouldSimulateFailure bool
}

func (arw *ApiReaderWrapper) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	if arw.ShouldSimulateFailure {
		return errors.New("simulation of api reader error")
	}
	return arw.apiReader.Get(ctx, key, obj)
}

func (arw *ApiReaderWrapper) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if arw.ShouldSimulateFailure {
		return errors.New("simulation of api reader error")
	}

	return arw.apiReader.List(ctx, list)
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

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	apiReaderWrapper = ApiReaderWrapper{
		apiReader:             k8sManager.GetAPIReader(),
		ShouldSimulateFailure: false,
	}

	err = (&PoisonPillConfigReconciler{
		Client:            k8sManager.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("poison-pill-config-controller"),
		InstallFileFolder: "../install/",
		Scheme:            scheme.Scheme,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	dummyDog, err = watchdog.NewFake(ctrl.Log.WithName("watchdog"))
	Expect(err).ToNot(HaveOccurred())
	err = k8sManager.Add(dummyDog)
	Expect(err).ToNot(HaveOccurred())

	err = (&PoisonPillRemediationReconciler{
		Client:                       k8sManager.GetClient(),
		Log:                          ctrl.Log.WithName("controllers").WithName("poison-pill-remediation-controller"),
		ApiReader:                    &apiReaderWrapper,
		Watchdog:                     dummyDog,
		SafeTimeToAssumeNodeRebooted: 90 * time.Second,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	reconcileInterval = 1 * time.Second
	myNodeName = "node1"

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctrl.SetupSignalHandler())
		Expect(err).ToNot(HaveOccurred())
	}()

	err = poisonpillv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient = k8sManager.GetClient()
	Expect(k8sClient).ToNot(BeNil())

}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())

	Expect(os.Unsetenv(envVarApiServer)).To(Succeed())
	Expect(os.Unsetenv(envVarETCD)).To(Succeed())
	Expect(os.Unsetenv(envVarKUBECTL)).To(Succeed())
})
