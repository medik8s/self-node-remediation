package watchdog_test

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/self-node-remediation/internal/watchdog"
)

var _ = Describe("Watchdog", func() {

	var wd watchdog.FakeWatchdog
	var cancel context.CancelFunc

	BeforeEach(func() {
		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())

		wd = watchdog.NewFake(true)
		Expect(wd).NotTo(BeNil())
		go func() {
			err := wd.Start(ctx)
			Expect(err).NotTo(HaveOccurred())
		}()
		Eventually(func(g Gomega) {
			g.Expect(wd.Status()).To(Equal(watchdog.Armed))
		}, 1*time.Second, 100*time.Millisecond).Should(Succeed(), "watchdog should be armed")
	})

	AfterEach(func() {
		cancel()
	})

	Context("Watchdog started", func() {
		It("should be fed", func() {
			verifyWatchdogFood(wd)
		})
	})

	Context("Watchdog triggered", func() {
		BeforeEach(func() {
			wd.Stop()
		})

		It("should be triggered and not be fed anymore", func() {
			Eventually(func(g Gomega) {
				g.Expect(wd.Status()).To(Equal(watchdog.Triggered))
			}, 1*time.Second, 100*time.Millisecond).Should(Succeed(), "watchdog should be triggered")
			verifyNoWatchdogFood(wd)
		})
	})

	Context("Watchdog cancelled", func() {
		BeforeEach(func() {
			cancel()
		})

		It("should be disarmed and and not be fed anymore", func() {
			Eventually(func(g Gomega) {
				g.Expect(wd.Status()).To(Equal(watchdog.Disarmed))
			}, 1*time.Second, 100*time.Millisecond).Should(Succeed(), "watchdog should be disarmed")
			verifyNoWatchdogFood(wd)
		})
	})

	Context("Triggered watchdog reset", func() {
		BeforeEach(func() {
			wd.Stop()
			wd.Reset()
		})

		It("should be armed and fed", func() {
			Eventually(func(g Gomega) {
				g.Expect(wd.Status()).To(Equal(watchdog.Armed))
			}, 1*time.Second, 100*time.Millisecond).Should(Succeed(), "watchdog should be armed")
			verifyWatchdogFood(wd)
		})
	})
})

var _ = Describe("Watchdog start signalling", func() {
	Context("watchdog starts successfully", func() {
		It("should close the Started channel and expose the timeout", func(ctx SpecContext) {
			wd := watchdog.NewFake(true)
			Expect(wd.Started()).NotTo(BeClosed())
			// the timeout is only known after start
			Expect(wd.GetTimeout()).To(BeZero())

			go func() {
				defer GinkgoRecover()
				Expect(wd.Start(ctx)).To(Succeed())
			}()

			Eventually(wd.Started(), 1*time.Second).Should(BeClosed())
			Expect(wd.Status()).To(Equal(watchdog.Armed))
			Expect(wd.GetTimeout()).To(Equal(1 * time.Second))
		})
	})

	Context("watchdog start fails with software reboot enabled", func() {
		BeforeEach(func() {
			Expect(os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "true")).To(Succeed())
			DeferCleanup(os.Unsetenv, watchdog.IsSoftwareRebootEnabledEnvVar)
		})

		It("should still close the Started channel", func(ctx SpecContext) {
			wd := watchdog.NewFake(false)

			go func() {
				defer GinkgoRecover()
				Expect(wd.Start(ctx)).To(Succeed())
			}()

			Eventually(wd.Started(), 1*time.Second).Should(BeClosed())
			Expect(wd.Status()).To(Equal(watchdog.Malfunction))
			Expect(wd.GetTimeout()).To(BeZero())
		})
	})

	Context("watchdog start fails with software reboot disabled", func() {
		BeforeEach(func() {
			Expect(os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "false")).To(Succeed())
			DeferCleanup(os.Unsetenv, watchdog.IsSoftwareRebootEnabledEnvVar)
		})

		It("should close the Started channel so callers are not blocked", func(ctx SpecContext) {
			wd := watchdog.NewFake(false)

			go func() {
				defer GinkgoRecover()
				err := wd.Start(ctx)
				Expect(err).To(HaveOccurred())
			}()

			Eventually(wd.Started(), 1*time.Second).Should(BeClosed())
			Expect(wd.Status()).To(Equal(watchdog.Disarmed))
			Expect(wd.GetTimeout()).To(BeZero())
		})
	})

	Context("watchdog start fails with IsSoftwareRebootEnabled parse error", func() {
		BeforeEach(func() {
			Expect(os.Setenv(watchdog.IsSoftwareRebootEnabledEnvVar, "not-a-bool")).To(Succeed())
			DeferCleanup(os.Unsetenv, watchdog.IsSoftwareRebootEnabledEnvVar)
		})

		It("should close the Started channel so callers are not blocked", func(ctx SpecContext) {
			wd := watchdog.NewFake(false)

			go func() {
				defer GinkgoRecover()
				err := wd.Start(ctx)
				Expect(err).To(HaveOccurred())
			}()

			Eventually(wd.Started(), 1*time.Second).Should(BeClosed())
			Expect(wd.Status()).To(Equal(watchdog.Disarmed))
			Expect(wd.GetTimeout()).To(BeZero())
		})
	})
})

func verifyWatchdogFood(wd watchdog.Watchdog) {
	currentLastFoodTime := wd.LastFoodTime()
	EventuallyWithOffset(1, func() time.Time {
		return wd.LastFoodTime()
	}, wd.GetTimeout(), 100*time.Millisecond).Should(BeTemporally(">", currentLastFoodTime), "watchdog should receive food")
}

func verifyNoWatchdogFood(wd watchdog.Watchdog) {
	currentLastFoodTime := wd.LastFoodTime()
	ConsistentlyWithOffset(1, func() time.Time {
		return wd.LastFoodTime()
	}, 5*wd.GetTimeout(), 1*time.Second).Should(Equal(currentLastFoodTime), "watchdog should not receive food anymore")
}
