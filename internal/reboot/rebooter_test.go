package reboot

import (
	"context"
	"os"
	"time"

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
})

var _ = Describe("Rebooter with a watchdog armed only for a reboot", func() {
	var rebooter *watchdogRebooter
	var wd watchdog.FakeWatchdog

	startStandby := func(isStartSuccessful bool) {
		wd = watchdog.NewStandbyFake(isStartSuccessful)
		rebooter = &watchdogRebooter{wd, ctrl.Log.WithName("fake rebooter"), fakeSoftwareReboot}
		go func() {
			defer GinkgoRecover()
			Expect(wd.Start(context.Background())).To(Succeed())
		}()
		Eventually(wd.Started(), 1*time.Second).Should(BeClosed())
		Expect(wd.Status()).To(Equal(watchdog.Standby))
	}

	AfterEach(func() {
		isSoftwareRebootCalled = false
	})

	Context("the device can be opened", func() {
		BeforeEach(func() { startStandby(true) })

		It("arms the watchdog and does not reboot by software", func() {
			Expect(rebooter.Reboot()).To(Succeed())
			Expect(wd.Status()).To(Equal(watchdog.Triggered))
			Expect(wd.StartCount()).To(Equal(1))
			Expect(isSoftwareRebootCalled).To(BeFalse())

			By("a repeated request right after keeps waiting for the watchdog")
			Expect(rebooter.Reboot()).To(Succeed())
			Expect(wd.StartCount()).To(Equal(1))
			Expect(isSoftwareRebootCalled).To(BeFalse())
		})
	})

	Context("the device cannot be opened", func() {
		BeforeEach(func() { startStandby(false) })

		It("falls back to a software reboot", func() {
			Expect(rebooter.Reboot()).To(Succeed())
			Expect(wd.Status()).To(Equal(watchdog.Malfunction))
			Expect(isSoftwareRebootCalled).To(BeTrue())
		})
	})
})

func fakeSoftwareReboot() error {
	isSoftwareRebootCalled = true
	return nil
}
