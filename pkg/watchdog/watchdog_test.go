package watchdog_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/self-node-remediation/pkg/watchdog"
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
