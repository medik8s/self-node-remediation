/*


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
	wdt "github.com/n1r1/poison-pill/watchdog"
	"github.com/onsi/gomega/gexec"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

const (
	machineNamespace = "openshift-machine-api"
)

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var dummyDog wdt.DummyWatchdog
var apiReaderWrapper ApiReaderWrapper

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

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

var _ = BeforeSuite(func(done Done) {
	//logf.SetLogger(zap.New(GinkgoWriter, true))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	err = machinev1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	apiReaderWrapper = ApiReaderWrapper{
		apiReader:             k8sManager.GetAPIReader(),
		ShouldSimulateFailure: false,
	}

	err = (&MachineReconciler{
		Client:    k8sManager.GetClient(),
		Log:       ctrl.Log.WithName("controllers").WithName("poison-pill-controller"),
		ApiReader: &apiReaderWrapper,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	reconcileInterval = 1 * time.Second
	dummyDog = wdt.DummyWatchdog{}
	watchdog = dummyDog
	myNodeName = "node1"

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctrl.SetupSignalHandler())
		Expect(err).ToNot(HaveOccurred())
	}()

	k8sClient = k8sManager.GetClient()
	Expect(k8sClient).ToNot(BeNil())

	ns := &v1.Namespace{}
	ns.Name = machineNamespace
	err = k8sClient.Create(context.TODO(), ns)
	Expect(err).ToNot(HaveOccurred())

	machineName := "machine1"

	node1 := &v1.Node{}
	node1.Name = myNodeName
	node1.Annotations = make(map[string]string)
	node1.Annotations[machineAnnotationKey] = machineNamespace + "/" + machineName
	Expect(k8sClient.Create(context.TODO(), node1)).To(Succeed())

	Expect(os.Setenv(nodeNameEnvVar, node1.Name)).To(Succeed())

	machine1 := &machinev1beta1.Machine{}
	machine1.Name = machineName
	machine1.Namespace = machineNamespace

	machine1.Status.NodeRef = &v1.ObjectReference{
		Kind:      "Node",
		Name:      node1.Name,
		Namespace: "",
	}
	Expect(machine1.Status.NodeRef).ToNot(BeNil())

	Expect(k8sClient.Create(context.TODO(), machine1)).To(Succeed())
	//this is required in order to update status.nodeRef
	Expect(k8sClient.Status().Update(context.Background(), machine1))
	Expect(machine1.Status.NodeRef).ToNot(BeNil())

	machine1 = &machinev1beta1.Machine{}
	nsName := types.NamespacedName{
		Name:      machineName,
		Namespace: machineNamespace,
	}

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), nsName, machine1)
		if err != nil {
			return false
		}
		return true
	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())

	Expect(machine1.Name).To(Equal("machine1"))
	Expect(machine1.Status.NodeRef).ToNot(BeNil())

	close(done)
}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	gexec.KillAndWait(5 * time.Second)
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
