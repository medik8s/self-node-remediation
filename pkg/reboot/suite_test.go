package reboot

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
)

func TestWatchdog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		"Rebooter Suite",
		[]Reporter{printer.NewlineReporter{}})
}

var _ = BeforeSuite(func() {

}, 60)

var _ = AfterSuite(func() {

})
