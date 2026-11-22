package reboot

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/self-node-remediation/v5/internal/watchdog"
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

	Describe("Software reboot methods", func() {
		var dir, trigger, marker string

		BeforeEach(func() {
			rebooter = &watchdogRebooter{log: ctrl.Log.WithName("fake rebooter")}
			dir = GinkgoT().TempDir()
			trigger = filepath.Join(dir, "sysrq-trigger")
			marker = filepath.Join(dir, "command-ran")
			Expect(os.WriteFile(trigger, nil, 0600)).To(Succeed())
		})

		It("should write b to the sysrq trigger and run no commands", func() {
			Expect(rebooter.runRebootMethods([]rebootMethod{
				sysrqMethod(trigger),
				commandMethod("touch", marker),
			})).To(Succeed())
			Expect(os.ReadFile(trigger)).To(Equal([]byte("b")))
			Expect(marker).ToNot(BeAnExistingFile())
		})

		It("should fall through to the commands when the sysrq write fails", func() {
			Expect(rebooter.runRebootMethods([]rebootMethod{
				sysrqMethod(filepath.Join(dir, "nonexistent", "sysrq-trigger")),
				commandMethod("false"),
				commandMethod("touch", marker),
			})).To(Succeed())
			Expect(marker).To(BeAnExistingFile())
		})

		It("should return an error naming each method when all fail", func() {
			missing := filepath.Join(dir, "nonexistent", "sysrq-trigger")
			err := rebooter.runRebootMethods([]rebootMethod{
				sysrqMethod(missing),
				commandMethod("false"),
				commandMethod("/nonexistent/reboot", "-f"),
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("all software reboot methods failed"))
			Expect(err.Error()).To(ContainSubstring("write b to " + missing))
			Expect(err.Error()).To(ContainSubstring("false: exit status 1"))
			Expect(err.Error()).To(ContainSubstring("/nonexistent/reboot -f"))
		})

		It("should try the sysrq write after systemctl and before reboot -f", func() {
			var names []string
			for _, m := range softwareRebootMethods() {
				names = append(names, m.name)
			}
			Expect(names).To(Equal([]string{
				"systemctl reboot --force --force",
				"nsenter -m/proc/1/ns/mnt -- systemctl reboot --force --force",
				"write b to /proc/sysrq-trigger",
				"reboot -f",
				"nsenter -m/proc/1/ns/mnt -- reboot -f",
				"nsenter -m/proc/1/ns/mnt /bin/bash -c echo b > /proc/sysrq-trigger",
			}))
		})
	})
})

func fakeSoftwareReboot() error {
	isSoftwareRebootCalled = true
	return nil
}
