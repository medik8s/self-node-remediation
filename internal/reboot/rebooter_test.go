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
			rebooter = &watchdogRebooter{wd, ctrl.Log.WithName("fake rebooter"), fakeSoftwareReboot}

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
	})
})

func fakeSoftwareReboot() error {
	isSoftwareRebootCalled = true
	return nil
}

var _ = Describe("Software reboot command override", func() {
	var rebooter *watchdogRebooter

	BeforeEach(func() {
		rebooter = &watchdogRebooter{
			log: ctrl.Log.WithName("override rebooter"),
		}
		// Use the real softwareReboot method (not the fake hook)
		rebooter.softwareRebootHook = rebooter.softwareReboot
	})

	Context("when REBOOT_COMMAND_OVERRIDE is set", func() {
		BeforeEach(func() {
			prev, existed := os.LookupEnv(rebootCommandOverrideEnvVar)
			Expect(os.Setenv(rebootCommandOverrideEnvVar, "/bin/true")).To(Succeed())
			DeferCleanup(func() {
				if existed {
					os.Setenv(rebootCommandOverrideEnvVar, prev)
				} else {
					os.Unsetenv(rebootCommandOverrideEnvVar)
				}
			})
		})

		It("should run the override command successfully", func() {
			err := rebooter.softwareReboot()
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("when REBOOT_COMMAND_OVERRIDE is set to an invalid command", func() {
		BeforeEach(func() {
			prev, existed := os.LookupEnv(rebootCommandOverrideEnvVar)
			Expect(os.Setenv(rebootCommandOverrideEnvVar, "/nonexistent/command")).To(Succeed())
			DeferCleanup(func() {
				if existed {
					os.Setenv(rebootCommandOverrideEnvVar, prev)
				} else {
					os.Unsetenv(rebootCommandOverrideEnvVar)
				}
			})
		})

		It("should not return an error but log it", func() {
			// softwareReboot logs errors but always returns nil
			err := rebooter.softwareReboot()
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
