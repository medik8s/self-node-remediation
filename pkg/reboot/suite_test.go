package reboot

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWatchdog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rebooter Suite")
}

var _ = BeforeSuite(func() {

})

var _ = AfterSuite(func() {

})
