package reboot

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/self-node-remediation/internal/watchdog"
)

var isSoftwareRebootCalled bool

var _ = Describe("Rebooter tests", func() {
	var rebooter *watchdogRebooter

	Describe("Crash on start", func() {
		BeforeEach(func() {
			wd := watchdog.NewFake(false)
			rebooter = &watchdogRebooter{
				wd:                 wd,
				log:                ctrl.Log.WithName("fake rebooter"),
				softwareRebootHook: fakeSoftwareReboot,
			}

		})

		AfterEach(func() {
			isSoftwareRebootCalled = false
		})

		Context("Software reboot is not configured", func() {
			It("watchdog should not start", func() {
				wd := rebooter.wd
				err := wd.Start(context.TODO())
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("software reboot check failed"))
				Expect(wd.Status()).To(Equal(watchdog.Disarmed))
			})
		})

		Context("Software reboot is disabled", func() {
			BeforeEach(func() {
				_ = os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "false")
			})
			AfterEach(func() {
				_ = os.Unsetenv(watchdog.IsSoftwareRebootEnabledEnvVar)
			})
			It("watchdog should not start", func() {
				wd := rebooter.wd
				err := wd.Start(context.TODO())
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("software reboot is disabled"))
				Expect(wd.Status()).To(Equal(watchdog.Disarmed))
			})
		})

		Context("Software reboot is enabled", func() {
			BeforeEach(func() {
				_ = os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "true")
			})
			AfterEach(func() {
				_ = os.Unsetenv(watchdog.IsSoftwareRebootEnabledEnvVar)
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

		Context("watchdog is unavailable and software reboot is disabled", func() {
			BeforeEach(func() {
				Expect(os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "false")).To(Succeed())
				rebooter.wd = nil
			})
			AfterEach(func() {
				Expect(os.Unsetenv(watchdog.IsSoftwareRebootEnabledEnvVar)).To(Succeed())
			})

			It("must fail closed without invoking the reboot hook", func() {
				Expect(rebooter.Reboot()).To(MatchError("software reboot is disabled"))
				Expect(isSoftwareRebootCalled).To(BeFalse())
			})
		})
	})
})

func fakeSoftwareReboot() error {
	isSoftwareRebootCalled = true
	return nil
}
