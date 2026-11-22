package apicheck

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/v5/api"
	"github.com/medik8s/self-node-remediation/v5/internal/certificates"
	"github.com/medik8s/self-node-remediation/v5/internal/peerhealth"
	"github.com/medik8s/self-node-remediation/v5/internal/peers"
	snrwebhook "github.com/medik8s/self-node-remediation/v5/internal/webhook/v1alpha1"
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

	DescribeTable("getHealthStatusFromPeer connects to IP literals",
		func(network, ip string) {
			listener, err := net.Listen(network, net.JoinHostPort(ip, "0"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = listener.Close() })

			caPem, certPem, keyPem, err := certificates.CreateCerts()
			Expect(err).NotTo(HaveOccurred())
			config.CertReader = &certificates.MemoryCertStorage{
				CaPem:   caPem,
				CertPem: certPem,
				KeyPem:  keyPem,
			}
			config.PeerHealthPort = listener.Addr().(*net.TCPAddr).Port
			config.PeerDialTimeout = 5 * time.Second

			serverCreds, err := certificates.GetServerCredentialsFromCerts(config.CertReader)
			Expect(err).NotTo(HaveOccurred())
			server := grpc.NewServer(grpc.Creds(serverCreds))
			peerhealth.RegisterPeerHealthServer(server, &healthyPeerServer{})
			DeferCleanup(server.Stop)
			go func() {
				defer GinkgoRecover()
				Expect(server.Serve(listener)).To(Succeed())
			}()

			results := make(chan peerResponse, 1)
			apiCheck.getHealthStatusFromPeer(corev1.PodIP{IP: ip}, results)

			Expect((<-results).code).To(Equal(selfNodeRemediation.Healthy))
		},
		Entry("IPv4", "tcp4", "127.0.0.1"),
		Entry("compressed IPv6", "tcp6", "::1"),
		Entry("expanded IPv6", "tcp6", "0:0:0:0:0:0:0:1"),
	)

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

func (f *fakePeers) RefreshControlPlanePeersForIsolationCheck(_ context.Context) error {
	return nil
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

	// answer returns a static per-peer answer, and tags same-domain api errors and silences like sumPeersResponses
	// does; a peer without an answer cannot be dialled, which is how a partition looks
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
			case selfNodeRemediation.RequestFailed:
				r.noResponse++
				if inMyDomain[a.IP] {
					r.sameDomainNoResponse++
				}
			default:
				r.unreachable++
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

	Context("when fewer peers than MinPeersForRemediation live outside of our domain", func() {
		// MinPeersForRemediation = 2 with one peer in our domain and one outside of it: only one peer would vote
		BeforeEach(func() {
			apiCheck.config.MinPeersForRemediation = 2
			fake.addresses = []corev1.PodIP{{IP: sameDomainPeer}, {IP: otherDomainPeer}}
		})

		It("keeps the legacy behavior: an api error from the same-domain peer proves we are not isolated", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError // the peer of the other domain does not answer
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeTrue())
			Expect(r.Reason).To(Equal(peers.HealthyBecauseNoPeersResponseNotReachedTimeout))
		})
	})

	Context("when exactly MinPeersForRemediation peers live outside of our domain", func() {
		BeforeEach(func() { apiCheck.config.MinPeersForRemediation = 2 }) // two of the three peers are outside zone-a

		It("ignores the api error of the same-domain peer and detects the isolation", func() {
			responses[sameDomainPeer] = selfNodeRemediation.ApiError // peers of other domains do not answer
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
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

		It("ignores a silent peer of our own domain, like an api error from it", func() {
			responses[sameDomainPeer] = selfNodeRemediation.RequestFailed // peers of other domains cannot be dialled
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
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

type healthyPeerServer struct {
	peerhealth.UnimplementedPeerHealthServer
}

func (*healthyPeerServer) IsHealthy(context.Context, *peerhealth.HealthRequest) (*peerhealth.HealthResponse, error) {
	return &peerhealth.HealthResponse{Status: int32(selfNodeRemediation.Healthy)}, nil
}

var _ = Describe("getWorkerPeersResponse with silent and unreachable peers", func() {
	const (
		peer1 = "10.0.0.1"
		peer2 = "10.0.0.2"
		peer3 = "10.0.0.3"
		peer4 = "10.0.0.4"
		peer5 = "10.0.0.5"
	)

	var (
		apiCheck  *ApiConnectivityCheck
		fake      *fakePeers
		responses map[string]selfNodeRemediation.HealthCheckResponseCode
		asked     [][]string
	)

	// answer returns a static per-peer answer and records the batches; a peer without an answer cannot be dialled
	answer := func(addresses []corev1.PodIP, _ map[string]bool) peersResponses {
		batch := []string{}
		r := peersResponses{}
		for _, a := range addresses {
			batch = append(batch, a.IP)
			switch responses[a.IP] {
			case selfNodeRemediation.Healthy:
				r.healthy++
			case selfNodeRemediation.Unhealthy:
				r.unhealthy++
			case selfNodeRemediation.ApiError:
				r.apiErrors++
			case selfNodeRemediation.RequestFailed:
				r.noResponse++
			default:
				r.unreachable++
			}
		}
		asked = append(asked, batch)
		return r
	}

	BeforeEach(func() {
		fake = &fakePeers{addresses: []corev1.PodIP{{IP: peer1}, {IP: peer2}, {IP: peer3}}}
		responses = map[string]selfNodeRemediation.HealthCheckResponseCode{}
		asked = nil
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

	It("treats peers that cannot be dialled as a sign of isolation", func() {
		r := apiCheck.getWorkerPeersResponse()
		Expect(r.IsHealthy).To(BeFalse())
		Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
	})

	It("counts a majority of reachable but silent peers as a control plane failure", func() {
		responses[peer1] = selfNodeRemediation.RequestFailed
		responses[peer2] = selfNodeRemediation.RequestFailed // peer3 cannot be dialled
		r := apiCheck.getWorkerPeersResponse()
		Expect(r.IsHealthy).To(BeTrue())
		Expect(r.Reason).To(Equal(peers.HealthyBecauseMostPeersCantAccessAPIServer))
	})

	It("adds silent peers to the peers reporting an api error", func() {
		responses[peer1] = selfNodeRemediation.ApiError
		responses[peer2] = selfNodeRemediation.RequestFailed // peer3 cannot be dialled
		r := apiCheck.getWorkerPeersResponse()
		Expect(r.IsHealthy).To(BeTrue())
		Expect(r.Reason).To(Equal(peers.HealthyBecauseMostPeersCantAccessAPIServer))
	})

	It("does not let a silent minority decide", func() {
		responses[peer1] = selfNodeRemediation.RequestFailed // peer2 and peer3 cannot be dialled
		r := apiCheck.getWorkerPeersResponse()
		Expect(r.IsHealthy).To(BeFalse())
		Expect(r.Reason).To(Equal(peers.UnHealthyBecauseNodeIsIsolated))
	})

	It("does not reset the no-peers-response timer on silence", func() {
		responses[peer1] = selfNodeRemediation.RequestFailed
		before := apiCheck.timeOfLastPeerResponse
		apiCheck.getWorkerPeersResponse()
		Expect(apiCheck.timeOfLastPeerResponse).To(Equal(before))
	})

	Context("when connections to the first batch break before the peers answer", func() {
		// the regression raised in review: a remediation exists for this node, two of five peers can read it,
		// and the connections to the first three break after the dial
		BeforeEach(func() {
			fake.addresses = []corev1.PodIP{{IP: peer1}, {IP: peer2}, {IP: peer3}, {IP: peer4}, {IP: peer5}}
			responses[peer1] = selfNodeRemediation.PeerUnreachable
			responses[peer2] = selfNodeRemediation.PeerUnreachable
			responses[peer3] = selfNodeRemediation.PeerUnreachable
			responses[peer4] = selfNodeRemediation.Unhealthy
			responses[peer5] = selfNodeRemediation.Unhealthy
		})

		It("keeps asking the remaining peers and follows their verdict", func() {
			r := apiCheck.getWorkerPeersResponse()
			Expect(r.IsHealthy).To(BeFalse())
			Expect(r.Reason).To(Equal(peers.UnHealthyBecausePeersResponse))
			Expect(asked).To(HaveLen(2), "the first batch must not end the round")
			Expect(asked[0]).To(ConsistOf(peer1, peer2, peer3))
			Expect(asked[1]).To(ConsistOf(peer4, peer5))
		})
	})
})

var _ = Describe("peerFailureCode", func() {
	It("treats a deadline exceeded as a reachable peer that stayed silent", func() {
		Expect(peerFailureCode(status.Error(codes.DeadlineExceeded, "context deadline exceeded"))).To(Equal(selfNodeRemediation.RequestFailed))
		Expect(peerFailureCode(context.DeadlineExceeded)).To(Equal(selfNodeRemediation.RequestFailed))
	})

	It("treats any other failure as an unreachable peer", func() {
		Expect(peerFailureCode(status.Error(codes.Unavailable, "transport is closing"))).To(Equal(selfNodeRemediation.PeerUnreachable))
		Expect(peerFailureCode(status.Error(codes.Canceled, "canceled"))).To(Equal(selfNodeRemediation.PeerUnreachable))
		Expect(peerFailureCode(errors.New("connection reset by peer"))).To(Equal(selfNodeRemediation.PeerUnreachable))
	})
})

var _ = Describe("sumPeersResponses", func() {
	var apiCheck *ApiConnectivityCheck

	BeforeEach(func() {
		apiCheck = &ApiConnectivityCheck{
			config: &ApiConnectivityCheckConfig{
				Log: ctrl.Log.WithName("test"),
			},
		}
	})

	sum := func(inMyDomain map[string]bool, answers ...peerResponse) peersResponses {
		ch := make(chan peerResponse, len(answers))
		for _, a := range answers {
			ch <- a
		}
		return apiCheck.sumPeersResponses(len(answers), ch, inMyDomain)
	}

	It("counts a peer that was dialled but stayed silent separately from one that could not be dialled", func() {
		r := sum(nil,
			peerResponse{peer: corev1.PodIP{IP: "10.0.0.1"}, code: selfNodeRemediation.RequestFailed},
			peerResponse{peer: corev1.PodIP{IP: "10.0.0.2"}, code: selfNodeRemediation.PeerUnreachable},
			peerResponse{peer: corev1.PodIP{IP: "10.0.0.3"}, code: selfNodeRemediation.PeerUnreachable},
		)
		Expect(r.noResponse).To(Equal(1), "RequestFailed means the dial succeeded and the peer stayed silent")
		Expect(r.unreachable).To(Equal(2), "PeerUnreachable means the dial failed or the connection broke")
	})

	It("tags silent peers of our own domain", func() {
		r := sum(map[string]bool{"10.0.0.1": true},
			peerResponse{peer: corev1.PodIP{IP: "10.0.0.1"}, code: selfNodeRemediation.RequestFailed},
			peerResponse{peer: corev1.PodIP{IP: "10.0.0.2"}, code: selfNodeRemediation.RequestFailed},
		)
		Expect(r.noResponse).To(Equal(2))
		Expect(r.sameDomainNoResponse).To(Equal(1))
	})
})

var _ = Describe("HealthCheckResponseCode wire values", func() {
	It("keeps the codes the peer sends over gRPC unchanged", func() {
		// PeerUnreachable is synthesised client side. If it were added to the iota block it would renumber these
		// and silently change the gRPC contract.
		Expect(int(selfNodeRemediation.Healthy)).To(Equal(1))
		Expect(int(selfNodeRemediation.Unhealthy)).To(Equal(2))
		Expect(int(selfNodeRemediation.ApiError)).To(Equal(3))
		Expect(int(selfNodeRemediation.RequestFailed)).To(Equal(-1))
		Expect(int(selfNodeRemediation.PeerUnreachable)).To(Equal(-2))
	})
})
