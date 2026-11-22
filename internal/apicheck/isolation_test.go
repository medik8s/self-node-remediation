package apicheck

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/v5/api"
	"github.com/medik8s/self-node-remediation/v5/internal/peers"
)

// fakeClock is a manual clock: time only moves when a test moves it
type fakeClock struct {
	t time.Time
}

func (f *fakeClock) now() time.Time {
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.t = f.t.Add(d)
}

// fakeRebooter counts the reboot requests instead of rebooting
type fakeRebooter struct {
	reboots int
}

func (f *fakeRebooter) Reboot() error {
	f.reboots++
	return nil
}

var _ = Describe("isolation wait", func() {
	const (
		checkInterval = 15 * time.Second
		window        = 10 * time.Minute
		threshold     = 3
		peer1         = "10.0.0.1"
		peer2         = "10.0.0.2"
		peer3         = "10.0.0.3"
	)

	var (
		clock    *fakeClock
		rebooter *fakeRebooter
		fake     *fakePeers
		answers  map[string]selfNodeRemediation.HealthCheckResponseCode
		apiCheck *ApiConnectivityCheck
	)

	// answer returns a static per-peer answer; peers without one do not answer
	answer := func(addresses []corev1.PodIP, _ map[string]bool) peersResponses {
		r := peersResponses{}
		for _, a := range addresses {
			switch answers[a.IP] {
			case selfNodeRemediation.Healthy:
				r.healthy++
			case selfNodeRemediation.Unhealthy:
				r.unhealthy++
			case selfNodeRemediation.ApiError:
				r.apiErrors++
			default:
				r.noResponse++
			}
		}
		return r
	}

	// tick runs one iteration of the check loop with the given API server check result, then lets the check interval pass
	tick := func(apiServerReachable bool) {
		if apiServerReachable {
			apiCheck.handleApiCheckSuccess(context.Background())
		} else {
			apiCheck.handleApiCheckFailure(context.Background())
		}
		clock.advance(checkInterval)
	}

	// failUntil runs failing checks until the clock reaches the given time
	failUntil := func(t time.Time) {
		for clock.t.Before(t) {
			tick(false)
		}
	}

	BeforeEach(func() {
		agentStart := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
		clock = &fakeClock{t: agentStart}
		rebooter = &fakeRebooter{}
		fake = &fakePeers{addresses: []corev1.PodIP{{IP: peer1}, {IP: peer2}, {IP: peer3}}}
		answers = map[string]selfNodeRemediation.HealthCheckResponseCode{}

		apiCheck = New(&ApiConnectivityCheckConfig{
			Log:                       ctrl.Log.WithName("test"),
			MyNodeName:                "test-node",
			CheckInterval:             checkInterval,
			MaxErrorsThreshold:        threshold,
			MinPeersForRemediation:    1,
			MaxTimeForNoPeersResponse: window,
			ApiServerTimeout:          5 * time.Second,
			PeerRequestTimeout:        7 * time.Second,
			Peers:                     fake,
			Rebooter:                  rebooter,
			Recorder:                  record.NewFakeRecorder(10),
		}, nil)
		apiCheck.now = clock.now
		apiCheck.timeOfLastPeerResponse = agentStart
		apiCheck.healthStatusGetter = answer

		// the agent has been running for an hour, much longer than the window, when the API server is lost
		clock.advance(time.Hour)
	})

	// expectRebootAfterWindowFrom checks that nothing happens until the window elapsed since the given time, and that
	// the node reboots at the first check after that
	expectRebootAfterWindowFrom := func(start time.Time) {
		failUntil(start.Add(window))
		Expect(rebooter.reboots).To(BeZero(), "rebooted before the window elapsed")
		tick(false) // the window has elapsed exactly now, not yet after it
		Expect(rebooter.reboots).To(BeZero(), "rebooted when the window had just elapsed")
		tick(false)
		Expect(rebooter.reboots).To(Equal(1), "did not reboot once the window elapsed")
	}

	Context("when the API server is lost on a long-running agent and no peer answers", func() {
		It("waits the window from the start of the outage, then reboots", func() {
			expectRebootAfterWindowFrom(clock.t)
		})
	})

	Context("when there are no peers and MinPeersForRemediation is zero", func() {
		BeforeEach(func() {
			apiCheck.config.MinPeersForRemediation = 0
			fake.addresses = nil
		})

		It("waits the window from the start of the outage, then reboots", func() {
			expectRebootAfterWindowFrom(clock.t)
		})

		It("reports the isolation only once the window elapsed, even if a SelfNodeRemediation exists for the node", func() {
			// nobody can tell this node about its SelfNodeRemediation: the peer cache is empty
			outageStart := clock.t
			for i := 1; i < threshold; i++ {
				Expect(apiCheck.getWorkerPeersResponse().Reason).To(Equal(peers.HealthyBecauseErrorsThresholdNotReached))
				clock.advance(checkInterval)
			}
			Expect(apiCheck.getWorkerPeersResponse().Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))

			clock.t = outageStart.Add(window)
			Expect(apiCheck.getWorkerPeersResponse().Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))

			clock.advance(time.Second)
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
		})
	})

	Context("when there are no peers and MinPeersForRemediation is one, the default", func() {
		BeforeEach(func() {
			fake.addresses = nil
		})

		It("never considers the node isolated", func() {
			failUntil(clock.t.Add(2 * window))
			Expect(rebooter.reboots).To(BeZero())
			Expect(apiCheck.getWorkerPeersResponse().Reason).To(Equal(peers.HealthyBecauseNoPeersWereFound))
		})
	})

	Context("when the API server recovers during the window", func() {
		It("starts a fresh window at the next outage", func() {
			failUntil(clock.t.Add(window / 2))
			tick(true)
			Expect(rebooter.reboots).To(BeZero())

			expectRebootAfterWindowFrom(clock.t)
		})
	})

	Context("when a minority of the peers reports an API error for a long time, then every peer goes silent", func() {
		It("waits the window from the last peer answer", func() {
			answers[peer1] = selfNodeRemediation.ApiError
			failUntil(clock.t.Add(2 * window))
			Expect(rebooter.reboots).To(BeZero(), "rebooted while a peer was still answering")

			lastAnswer := apiCheck.timeOfLastPeerResponse
			Expect(lastAnswer).To(BeTemporally(">", apiCheck.outageStart))
			delete(answers, peer1)

			expectRebootAfterWindowFrom(lastAnswer)
		})
	})

	Context("when a peer answers that this node is healthy, then every peer goes silent", func() {
		It("closes the outage, so the silence waits a full window again", func() {
			answers[peer2] = selfNodeRemediation.Healthy
			failUntil(clock.t.Add(2 * window))
			Expect(rebooter.reboots).To(BeZero())
			// end on the check that reaches the threshold and gets the healthy answer
			for apiCheck.errorCount != 0 {
				tick(false)
			}
			Expect(apiCheck.outageStart.IsZero()).To(BeTrue())
			delete(answers, peer2)

			expectRebootAfterWindowFrom(clock.t)
		})
	})

	Context("when a peer answers that this node is unhealthy", func() {
		It("reboots as soon as the error threshold is reached, without waiting for the window", func() {
			answers[peer3] = selfNodeRemediation.Unhealthy
			for i := 1; i < threshold; i++ {
				tick(false)
			}
			Expect(rebooter.reboots).To(BeZero())
			tick(false)
			Expect(rebooter.reboots).To(Equal(1))
		})
	})
})
