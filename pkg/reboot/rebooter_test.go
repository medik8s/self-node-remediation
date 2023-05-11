package reboot

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/self-node-remediation/pkg/utils"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
)

var isSoftwareRebootCalled bool

var _ = Describe("Rebooter tests", func() {
	var rebooter *watchdogRebooter

	Describe("Crash on start", func() {
		BeforeEach(func() {
			wd, _ := watchdog.NewFake(false)
			rebooter = &watchdogRebooter{wd, ctrl.Log.WithName("fake rebooter"), fakeSoftwareReboot, nil}

		})

		AfterEach(func() {
			isSoftwareRebootCalled = false
		})

		Context("Software reboot is disabled", func() {
			It("watchdog should not start", func() {
				wd := rebooter.wd
				err := wd.Start(context.TODO())
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to start watchdog, can't default to software reboot"))
				Expect(wd.Status()).To(Equal(watchdog.Disarmed))
			})
		})

		Context("Software reboot is enabled", func() {
			BeforeEach(func() {
				_ = os.Setenv(utils.IsSoftwareRebootEnabledEnvVar, "true")
			})
			It("should return healthy", func() {
				wd := rebooter.wd
				err := wd.Start(context.TODO())
				Expect(err).ToNot(HaveOccurred())
				Expect(wd.Status()).To(Equal(watchdog.Malfunction))
				//Verify reboot goes as expected
				Expect(rebooter.Reboot()).ToNot(HaveOccurred())
				Expect(isSoftwareRebootCalled).To(BeTrue())
			})
		})

	})

})

func fakeSoftwareReboot() error {
	isSoftwareRebootCalled = true
	return nil
}
