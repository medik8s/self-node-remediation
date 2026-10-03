package apicheck

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/internal/peers"
	snrwebhook "github.com/medik8s/self-node-remediation/internal/webhook/v1alpha1"
)

func TestApiCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ApiCheck Suite")
}

var _ = Describe("ApiConnectivityCheck", func() {
	var (
		apiCheck     *ApiConnectivityCheck
		config       *ApiConnectivityCheckConfig
		fakeRecorder *record.FakeRecorder
		log          logr.Logger
	)

	BeforeEach(func() {
		log = ctrl.Log.WithName("test")
		fakeRecorder = record.NewFakeRecorder(10)

		config = &ApiConnectivityCheckConfig{
			Log:                log,
			MyNodeName:         "test-node",
			ApiServerTimeout:   5 * time.Second,
			PeerRequestTimeout: 7 * time.Second,
			Recorder:           fakeRecorder,
		}

		apiCheck = &ApiConnectivityCheck{
			config: config,
		}
	})

	Describe("getEffectivePeerRequestTimeout", func() {
		Context("when PeerRequestTimeout is safe", func() {
			It("should return the configured PeerRequestTimeout", func() {
				// ApiServerTimeout=5s, PeerRequestTimeout=7s, MinimumBuffer=2s
				// 7s >= (5s + 2s), so it's safe
				effectiveTimeout := apiCheck.getEffectivePeerRequestTimeout()

				Expect(effectiveTimeout).To(Equal(7 * time.Second))

				// Should not emit any events
				Expect(len(fakeRecorder.Events)).To(Equal(0))
			})
		})

		Context("when PeerRequestTimeout is unsafe", func() {
			It("should return adjusted timeout and emit warning event", func() {
				config.PeerRequestTimeout = 6 * time.Second // Less than 5s + 2s = 7s

				effectiveTimeout := apiCheck.getEffectivePeerRequestTimeout()

				expectedMinimumTimeout := config.ApiServerTimeout + snrwebhook.MinimumBuffer // 7s
				Expect(effectiveTimeout).To(Equal(expectedMinimumTimeout))

				// Should emit a warning event
				Expect(len(fakeRecorder.Events)).To(Equal(1))
				event := <-fakeRecorder.Events
				Expect(event).To(ContainSubstring("Warning"))
				Expect(event).To(ContainSubstring("PeerTimeoutAdjusted"))
				Expect(event).To(ContainSubstring("6s")) // configured timeout
				Expect(event).To(ContainSubstring("5s")) // API server timeout
				Expect(event).To(ContainSubstring("7s")) // safe timeout
			})

		})

	})
})

// fakePeers is a PeersProvider with a static peer list and static failure domains
type fakePeers struct {
	addresses []corev1.PodIP
	myDomain  string
	domains   map[string]string // ip -> domain
	cpDomains peers.ControlPlaneDomains
}

func (f *fakePeers) GetControlPlaneDomains() peers.ControlPlaneDomains {
	return f.cpDomains
}

func (f *fakePeers) GetPeersAddresses(_ peers.Role) []corev1.PodIP {
	out := make([]corev1.PodIP, len(f.addresses))
	copy(out, f.addresses)
	return out
}

func (f *fakePeers) GetPeersSnapshot(role peers.Role) peers.Snapshot {
	snapshot := peers.Snapshot{Addresses: f.GetPeersAddresses(role), InMyDomain: map[string]bool{}}
	if f.myDomain == "" {
		return snapshot
	}
	for _, a := range f.addresses {
		if domain, known := f.domains[a.IP]; known && domain == f.myDomain {
			snapshot.InMyDomain[a.IP] = true
		} else {
			snapshot.OutsideMyDomain++
		}
	}
	return snapshot
}

var _ = Describe("getWorkerPeersResponse with failure domain awareness", func() {
	const (
		sameDomainPeer   = "10.0.0.1" // same failure domain as the node under test
		otherDomainPeer  = "10.0.0.2"
		otherDomainPeer2 = "10.0.0.3"
		unlabeledPeer    = "10.0.0.4" // node without the topology label
	)

	var (
		apiCheck  *ApiConnectivityCheck
		fake      *fakePeers
		responses map[string]selfNodeRemediation.HealthCheckResponseCode
	)

	// answer returns a static per-peer answer, and tags same-domain api errors like sumPeersResponses does
	answer := func(addresses []corev1.PodIP, inMyDomain map[string]bool) peersResponses {
		r := peersResponses{}
		for _, a := range addresses {
			switch responses[a.IP] {
			case selfNodeRemediation.Healthy:
				r.healthy++
			case selfNodeRemediation.Unhealthy:
				r.unhealthy++
			case selfNodeRemediation.ApiError:
				r.apiErrors++
				if inMyDomain[a.IP] {
					r.sameDomainApiErrors++
				}
			default:
				r.noResponse++
			}
		}
		return r
	}

	BeforeEach(func() {
		fake = &fakePeers{
			addresses: []corev1.PodIP{{IP: sameDomainPeer}, {IP: otherDomainPeer}, {IP: otherDomainPeer2}},
			myDomain:  "zone-a",
			domains:   map[string]string{sameDomainPeer: "zone-a", otherDomainPeer: "zone-b", otherDomainPeer2: "zone-b"},
			// one control plane node per domain in a three domains cluster: the majority is outside of zone-a
			cpDomains: peers.ControlPlaneDomains{InMyDomain: 1, InOtherDomain: 2},
		}
		responses = map[string]selfNodeRemediation.HealthCheckResponseCode{}
		apiCheck = &ApiConnectivityCheck{
			config: &ApiConnectivityCheckConfig{
				Log:                       ctrl.Log.WithName("test"),
				MyNodeName:                "test-node",
				MaxErrorsThreshold:        1, // ask peers on first error
				MinPeersForRemediation:    1,
				MaxTimeForNoPeersResponse: 30 * time.Second,
				Peers:                     fake,
				Recorder:                  record.NewFakeRecorder(10),
			},
			// long enough ago for the "no peers response" timeout to be exceeded
			timeOfLastPeerResponse: time.Now().Add(-time.Hour),
		}
		apiCheck.healthStatusGetter = answer
	})

	Context("when the feature is disabled (node has no domain)", func() {
		BeforeEach(func() { fake.myDomain = "" })

		It("keeps the legacy behavior: an api error from any peer proves we are not isolated", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError // other peers unreachable
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})

		It("keeps the legacy behavior: api errors from a majority of peers mean a control plane failure", func() {
			for _, ip := range []string{sameDomainPeer, otherDomainPeer, otherDomainPeer2} {
				responses[ip] = selfNodeRemediation.ApiError
			}
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseMostPeersCantAccessAPIServer))
		})
	})

	Context("when all peers are in our own domain (single-domain cluster)", func() {
		BeforeEach(func() {
			fake.domains = map[string]string{sameDomainPeer: "zone-a", otherDomainPeer: "zone-a", otherDomainPeer2: "zone-a"}
		})

		It("falls back to the legacy behavior", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})
	})

	Context("when our whole failure domain is partitioned from the rest of the cluster", func() {
		It("ignores the api error of the same-domain peer and detects the isolation", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError // peers of other domains do not answer
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
		})

		It("does not reset the no-peers-response timer on a same-domain api error", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			before := apiCheck.timeOfLastPeerResponse
			apiCheck.getWorkerPeersResponse()
			Expect(apiCheck.timeOfLastPeerResponse).To(Equal(before))
		})
	})

	Context("when our own domain holds at least half of the control plane", func() {
		It("does not self-remediate when partitioned without an API server: no quorum can exist elsewhere", func() {
			fake.cpDomains = peers.ControlPlaneDomains{InMyDomain: 3}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError // peers of other domains do not answer
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})

		It("resets the no-peers-response timer on a same-domain api error, like the legacy evaluation", func() {
			fake.cpDomains = peers.ControlPlaneDomains{InMyDomain: 3}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			before := apiCheck.timeOfLastPeerResponse
			apiCheck.getWorkerPeersResponse()
			Expect(apiCheck.timeOfLastPeerResponse).To(BeTemporally(">", before))
		})

		It("treats an even split of the control plane as no majority outside", func() {
			fake.cpDomains = peers.ControlPlaneDomains{InMyDomain: 1, InOtherDomain: 1}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})

		It("does not count control plane nodes without the topology label as being outside", func() {
			fake.cpDomains = peers.ControlPlaneDomains{InOtherDomain: 1, Unknown: 2}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})

		It("does not self-remediate when the control plane nodes are unknown", func() {
			fake.cpDomains = peers.ControlPlaneDomains{}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})
	})

	Context("when a strict majority of the control plane is outside of our domain", func() {
		It("self-remediates when partitioned, even if the control plane is not spread evenly", func() {
			fake.cpDomains = peers.ControlPlaneDomains{InOtherDomain: 3}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
		})
	})

	Context("when the control plane is down but the network is fine", func() {
		It("counts api errors from peers of other domains as a control plane failure", func() {
			for _, ip := range []string{sameDomainPeer, otherDomainPeer, otherDomainPeer2} {
				responses[ip] = selfNodeRemediation.ApiError
			}
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseMostPeersCantAccessAPIServer))
		})

		It("computes the majority over the peers outside of our domain only", func() {
			// 2 voting peers (other domain), 1 api error is not a majority of them; but the timer is reset
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			responses[otherDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})

		It("treats a single remote peer reporting an api error as a majority", func() {
			fake.addresses = []corev1.PodIP{{IP: sameDomainPeer}, {IP: otherDomainPeer}}
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			responses[otherDomainPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseMostPeersCantAccessAPIServer))
		})
	})

	Context("when a same-domain peer has a definitive answer", func() {
		It("still trusts a healthy answer", func() {
			responses[sameDomainPeer] = selfNodeRemediation.Healthy
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseCRNotFound))
		})

		It("still trusts an unhealthy answer", func() {
			responses[sameDomainPeer] = selfNodeRemediation.Unhealthy
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecausePeersResponse))
		})
	})

	Context("when a peer node does not carry the topology label", func() {
		BeforeEach(func() {
			fake.addresses = []corev1.PodIP{{IP: sameDomainPeer}, {IP: unlabeledPeer}}
		})

		It("treats it as being outside of our domain, so its api error counts as evidence", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError
			responses[unlabeledPeer] = selfNodeRemediation.ApiError
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseMostPeersCantAccessAPIServer))
		})
	})
})

func TestControlPlaneMajorityOutside(t *testing.T) {
	testCases := []struct {
		name               string
		domains            peers.ControlPlaneDomains
		selfIsControlPlane bool
		expectMajority     bool
		expectInMyDomain   int
	}{
		{name: "one control plane per domain, three domains", domains: peers.ControlPlaneDomains{InMyDomain: 1, InOtherDomain: 2}, expectMajority: true, expectInMyDomain: 1},
		{name: "whole control plane in my domain", domains: peers.ControlPlaneDomains{InMyDomain: 3}, expectMajority: false, expectInMyDomain: 3},
		{name: "whole control plane in other domains", domains: peers.ControlPlaneDomains{InOtherDomain: 3}, expectMajority: true, expectInMyDomain: 0},
		{name: "even split is not a majority", domains: peers.ControlPlaneDomains{InMyDomain: 2, InOtherDomain: 2}, expectMajority: false, expectInMyDomain: 2},
		{name: "unlabeled nodes are not counted as outside", domains: peers.ControlPlaneDomains{InOtherDomain: 2, Unknown: 2}, expectMajority: false, expectInMyDomain: 0},
		{name: "no control plane node known", domains: peers.ControlPlaneDomains{}, expectMajority: false, expectInMyDomain: 0},
		{name: "own control plane node tips the balance", domains: peers.ControlPlaneDomains{InMyDomain: 0, InOtherDomain: 1}, selfIsControlPlane: true, expectMajority: false, expectInMyDomain: 1},
		{name: "own control plane node in the minority", domains: peers.ControlPlaneDomains{InOtherDomain: 2}, selfIsControlPlane: true, expectMajority: true, expectInMyDomain: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			domains, majority := controlPlaneMajorityOutside(tc.domains, tc.selfIsControlPlane)
			if majority != tc.expectMajority {
				t.Errorf("majority outside: expected %v, got %v", tc.expectMajority, majority)
			}
			if domains.InMyDomain != tc.expectInMyDomain {
				t.Errorf("control plane nodes in my domain: expected %d, got %d", tc.expectInMyDomain, domains.InMyDomain)
			}
		})
	}
}
