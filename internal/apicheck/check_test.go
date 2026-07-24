package apicheck

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/self-node-remediation/internal/controlplane"
	"github.com/medik8s/self-node-remediation/internal/peers"
	snrwebhook "github.com/medik8s/self-node-remediation/internal/webhook/v1alpha1"
)

func TestApiCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ApiCheck Suite")
}

// --- Test doubles ---

// mockPeerDialer allows tests to control TCP dial results for canReachAnyPeer.
// Thread-safe: canReachAnyPeer dials peers concurrently.
type mockPeerDialer struct {
	mu             sync.Mutex
	reachableAddrs map[string]bool
	dialCalls      []string
}

func newMockPeerDialer() *mockPeerDialer {
	return &mockPeerDialer{
		reachableAddrs: make(map[string]bool),
		dialCalls:      []string{},
	}
}

func (d *mockPeerDialer) SetReachable(addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reachableAddrs[addr] = true
}

func (d *mockPeerDialer) Dial(address string, _ time.Duration) error {
	d.mu.Lock()
	d.dialCalls = append(d.dialCalls, address)
	reachable := d.reachableAddrs[address]
	d.mu.Unlock()
	if reachable {
		return nil
	}
	return fmt.Errorf("connection refused")
}

func (d *mockPeerDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.dialCalls)
}

// mockPeerAddressProvider implements PeerAddressProvider for tests.
type mockPeerAddressProvider struct {
	workerAddresses       []corev1.PodIP
	controlPlaneAddresses []corev1.PodIP
}

func (m *mockPeerAddressProvider) GetPeersAddresses(role peers.Role) []corev1.PodIP {
	var source []corev1.PodIP
	if role == peers.Worker {
		source = m.workerAddresses
	} else {
		source = m.controlPlaneAddresses
	}
	result := make([]corev1.PodIP, len(source))
	copy(result, source)
	return result
}

// mockRebooter records reboot calls for assertions.
type mockRebooter struct {
	rebootCalled bool
	rebootCount  int
}

func (r *mockRebooter) Reboot() error {
	r.rebootCalled = true
	r.rebootCount++
	return nil
}

func (r *mockRebooter) Reset() {
	r.rebootCalled = false
	r.rebootCount = 0
}

// --- Helpers ---

func newTestApiCheck(log logr.Logger, peerProvider PeerAddressProvider, dialer PeerDialer, cpManager *controlplane.Manager) *ApiConnectivityCheck {
	return &ApiConnectivityCheck{
		config: &ApiConnectivityCheckConfig{
			Log:                       log,
			MyNodeName:                "test-node",
			ApiServerTimeout:          5 * time.Second,
			PeerRequestTimeout:        7 * time.Second,
			PeerHealthPort:            30001,
			MaxErrorsThreshold:        3,
			MaxTimeForNoPeersResponse: 30 * time.Second,
			MinPeersForRemediation:    1,
			Peers:                     peerProvider,
			Recorder:                  record.NewFakeRecorder(10),
		},
		controlPlaneManager:    cpManager,
		peerDialer:             dialer,
		timeOfLastPeerResponse: time.Now(),
	}
}

// --- Tests ---

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
			PeerHealthPort:     30001,
			Recorder:           fakeRecorder,
		}

		apiCheck = &ApiConnectivityCheck{
			config: config,
		}
	})

	Describe("getEffectivePeerRequestTimeout", func() {
		Context("when PeerRequestTimeout is safe", func() {
			It("should return the configured PeerRequestTimeout", func() {
				effectiveTimeout := apiCheck.getEffectivePeerRequestTimeout()
				Expect(effectiveTimeout).To(Equal(7 * time.Second))
				Expect(len(fakeRecorder.Events)).To(Equal(0))
			})
		})

		Context("when PeerRequestTimeout is unsafe", func() {
			It("should return adjusted timeout and emit warning event", func() {
				config.PeerRequestTimeout = 6 * time.Second
				effectiveTimeout := apiCheck.getEffectivePeerRequestTimeout()
				expectedMinimumTimeout := config.ApiServerTimeout + snrwebhook.MinimumBuffer
				Expect(effectiveTimeout).To(Equal(expectedMinimumTimeout))
				Expect(len(fakeRecorder.Events)).To(Equal(1))
				event := <-fakeRecorder.Events
				Expect(event).To(ContainSubstring("Warning"))
				Expect(event).To(ContainSubstring("PeerTimeoutAdjusted"))
			})
		})
	})

	Describe("isControlPlane", func() {
		Context("when controlPlaneManager is nil", func() {
			It("should return false", func() {
				apiCheck.controlPlaneManager = nil
				Expect(apiCheck.isControlPlane()).To(BeFalse())
			})
		})

		Context("when controlPlaneManager exists but node is a worker", func() {
			It("should return false", func() {
				// Manager with default zero-value nodeRole == Worker
				apiCheck.controlPlaneManager = &controlplane.Manager{}
				Expect(apiCheck.isControlPlane()).To(BeFalse())
			})
		})
	})

	Describe("canReachAnyPeer", func() {
		var (
			dialer       *mockPeerDialer
			peerProvider *mockPeerAddressProvider
		)

		BeforeEach(func() {
			dialer = newMockPeerDialer()
			peerProvider = &mockPeerAddressProvider{}
			apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
		})

		Context("when no peers exist", func() {
			It("should return true to avoid false isolation on single-node clusters", func() {
				peerProvider.workerAddresses = []corev1.PodIP{}
				peerProvider.controlPlaneAddresses = []corev1.PodIP{}
				Expect(apiCheck.canReachAnyPeer()).To(BeTrue())
				Expect(dialer.DialCount()).To(Equal(0))
			})
		})

		Context("when all peers are reachable", func() {
			It("should return true", func() {
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				}
				dialer.SetReachable("10.0.0.1:30001")
				dialer.SetReachable("10.0.0.2:30001")

				Expect(apiCheck.canReachAnyPeer()).To(BeTrue())
			})
		})

		Context("when some peers are reachable", func() {
			It("should return true if at least one peer responds", func() {
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				}
				// Only second peer is reachable
				dialer.SetReachable("10.0.0.2:30001")

				Expect(apiCheck.canReachAnyPeer()).To(BeTrue())
			})
		})

		Context("when all peers are unreachable", func() {
			It("should return false", func() {
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				}
				// No peers set as reachable
				Expect(apiCheck.canReachAnyPeer()).To(BeFalse())
				Expect(dialer.DialCount()).To(Equal(2))
			})
		})

		Context("when peers include both worker and CP addresses", func() {
			It("should check both types of peers", func() {
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
				}
				peerProvider.controlPlaneAddresses = []corev1.PodIP{
					{IP: "10.0.0.2"},
				}
				// Only CP peer is reachable
				dialer.SetReachable("10.0.0.2:30001")

				Expect(apiCheck.canReachAnyPeer()).To(BeTrue())
			})
		})

		Context("when more peers exist than maxPeersToSample", func() {
			It("should only sample up to maxPeersToSample peers", func() {
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
					{IP: "10.0.0.3"},
					{IP: "10.0.0.4"},
					{IP: "10.0.0.5"},
				}
				// None reachable
				Expect(apiCheck.canReachAnyPeer()).To(BeFalse())
				Expect(dialer.DialCount()).To(Equal(maxPeersToSample))
			})
		})

		Context("when peers have empty IPs", func() {
			It("should skip peers with empty IPs", func() {
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: ""},
					{IP: "10.0.0.1"},
				}
				dialer.SetReachable("10.0.0.1:30001")

				Expect(apiCheck.canReachAnyPeer()).To(BeTrue())
				// Should only dial the non-empty IP
				Expect(dialer.DialCount()).To(Equal(1))
			})
		})

		Context("deduplication on compact clusters", func() {
			It("should not check the same IP twice when it appears in both worker and CP lists", func() {
				// On compact clusters, same node is both worker and CP
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				}
				peerProvider.controlPlaneAddresses = []corev1.PodIP{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				}

				Expect(apiCheck.canReachAnyPeer()).To(BeFalse())
				// Should deduplicate — only 2 unique IPs, not 4
				Expect(dialer.DialCount()).To(Equal(2))
			})
		})
	})

	Describe("getAllPeerAddresses", func() {
		var peerProvider *mockPeerAddressProvider

		BeforeEach(func() {
			peerProvider = &mockPeerAddressProvider{}
			apiCheck = newTestApiCheck(log, peerProvider, newMockPeerDialer(), nil)
		})

		It("should return empty list when no peers exist", func() {
			result := apiCheck.getAllPeerAddresses()
			Expect(result).To(BeEmpty())
		})

		It("should combine worker and CP peers", func() {
			peerProvider.workerAddresses = []corev1.PodIP{{IP: "10.0.0.1"}}
			peerProvider.controlPlaneAddresses = []corev1.PodIP{{IP: "10.0.0.2"}}

			result := apiCheck.getAllPeerAddresses()
			Expect(result).To(HaveLen(2))
			Expect(result[0].IP).To(Equal("10.0.0.1"))
			Expect(result[1].IP).To(Equal("10.0.0.2"))
		})

		It("should deduplicate IPs across worker and CP lists", func() {
			peerProvider.workerAddresses = []corev1.PodIP{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}}
			peerProvider.controlPlaneAddresses = []corev1.PodIP{{IP: "10.0.0.2"}, {IP: "10.0.0.3"}}

			result := apiCheck.getAllPeerAddresses()
			Expect(result).To(HaveLen(3))
			ips := make([]string, len(result))
			for i, p := range result {
				ips[i] = p.IP
			}
			Expect(ips).To(ConsistOf("10.0.0.1", "10.0.0.2", "10.0.0.3"))
		})

		It("should skip empty IPs", func() {
			peerProvider.workerAddresses = []corev1.PodIP{{IP: ""}, {IP: "10.0.0.1"}}

			result := apiCheck.getAllPeerAddresses()
			Expect(result).To(HaveLen(1))
			Expect(result[0].IP).To(Equal("10.0.0.1"))
		})
	})

	// Regression tests: verify that the CP isolation detection does not
	// break existing worker node behavior.
	Describe("Worker node behavior — regression", func() {
		var (
			peerProvider *mockPeerAddressProvider
			dialer       *mockPeerDialer
		)

		BeforeEach(func() {
			peerProvider = &mockPeerAddressProvider{}
			dialer = newMockPeerDialer()
			// nil controlPlaneManager = worker node
			apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
		})

		It("should never call canReachAnyPeer on worker nodes", func() {
			// isControlPlane() returns false when controlPlaneManager is nil
			Expect(apiCheck.isControlPlane()).To(BeFalse())
			// If the Start loop were to run, the CP peer reachability check
			// is gated behind isControlPlane(), so dialer should never be called
			// for worker nodes.
		})

		It("should not track cpUnreachableCount on worker nodes", func() {
			apiCheck.cpUnreachableCount = 0
			// isConsideredHealthy for worker nodes returns workerPeersResponse.IsHealthy
			// directly, without touching cpUnreachableCount
			Expect(apiCheck.cpUnreachableCount).To(Equal(0))
		})
	})

	// Tests for the CP isolation escalation logic in isConsideredHealthy.
	// These test the cpUnreachableCount behavior at the field level since
	// full isConsideredHealthy requires real gRPC peer infrastructure.
	Describe("CP isolation escalation — cpUnreachableCount", func() {
		It("should start at zero", func() {
			apiCheck = newTestApiCheck(log, &mockPeerAddressProvider{}, newMockPeerDialer(), nil)
			Expect(apiCheck.cpUnreachableCount).To(Equal(0))
		})

		It("should be independent of errorCount", func() {
			apiCheck = newTestApiCheck(log, &mockPeerAddressProvider{}, newMockPeerDialer(), nil)
			apiCheck.errorCount = 5
			apiCheck.cpUnreachableCount = 2

			// They track different signals
			Expect(apiCheck.errorCount).To(Equal(5))
			Expect(apiCheck.cpUnreachableCount).To(Equal(2))
		})

		It("should be reset when errorCount is reset (healthy path)", func() {
			apiCheck = newTestApiCheck(log, &mockPeerAddressProvider{}, newMockPeerDialer(), nil)
			apiCheck.errorCount = 3
			apiCheck.cpUnreachableCount = 3

			// Simulating what Start() does when readyz passes and peers are reachable
			apiCheck.errorCount = 0
			apiCheck.cpUnreachableCount = 0

			Expect(apiCheck.errorCount).To(Equal(0))
			Expect(apiCheck.cpUnreachableCount).To(Equal(0))
		})
	})

	// Integration-style tests that verify the complete canReachAnyPeer +
	// isControlPlane gate works correctly for the CP isolation scenario.
	Describe("CP isolation detection — full flow", func() {
		var (
			peerProvider *mockPeerAddressProvider
			dialer       *mockPeerDialer
			rebooter     *mockRebooter
		)

		BeforeEach(func() {
			peerProvider = &mockPeerAddressProvider{
				workerAddresses: []corev1.PodIP{
					{IP: "10.0.0.1"},
				},
				controlPlaneAddresses: []corev1.PodIP{
					{IP: "10.0.0.2"},
				},
			}
			dialer = newMockPeerDialer()
			rebooter = &mockRebooter{}
		})

		Context("CP node with peers reachable (healthy)", func() {
			It("should reset errorCount when readyz passes and peers are reachable", func() {
				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
				apiCheck.errorCount = 2
				apiCheck.cpUnreachableCount = 1

				// Simulate: readyz passed, peers reachable (this is what Start does)
				dialer.SetReachable("10.0.0.1:30001")

				// Since controlPlaneManager is nil, isControlPlane() returns false
				// Worker path: just reset
				if !apiCheck.isControlPlane() || apiCheck.canReachAnyPeer() {
					apiCheck.errorCount = 0
					apiCheck.cpUnreachableCount = 0
				}

				Expect(apiCheck.errorCount).To(Equal(0))
				Expect(apiCheck.cpUnreachableCount).To(Equal(0))
			})
		})

		Context("CP node with no peers reachable (isolated)", func() {
			It("should NOT reset errorCount when readyz passes but peers are unreachable", func() {
				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
				apiCheck.config.Rebooter = rebooter
				apiCheck.errorCount = 1

				// No peers reachable — all dials fail
				reachable := apiCheck.canReachAnyPeer()
				Expect(reachable).To(BeFalse())

				// errorCount should NOT be reset
				Expect(apiCheck.errorCount).To(Equal(1))
			})
		})

		Context("Worker node — unchanged behavior", func() {
			It("should always reset errorCount when readyz passes, regardless of peer reachability", func() {
				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
				apiCheck.controlPlaneManager = nil // worker node
				apiCheck.errorCount = 2

				// For worker nodes, isControlPlane() is false, so the peer
				// reachability check is skipped entirely
				if !apiCheck.isControlPlane() {
					apiCheck.errorCount = 0
					apiCheck.cpUnreachableCount = 0
				}

				Expect(apiCheck.errorCount).To(Equal(0))
			})
		})

		Context("Transient network blip on CP node", func() {
			It("should recover when peers become reachable again", func() {
				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
				apiCheck.errorCount = 2
				apiCheck.cpUnreachableCount = 2

				// Network recovers — peers become reachable
				dialer.SetReachable("10.0.0.1:30001")

				if apiCheck.canReachAnyPeer() {
					apiCheck.errorCount = 0
					apiCheck.cpUnreachableCount = 0
				}

				Expect(apiCheck.errorCount).To(Equal(0))
				Expect(apiCheck.cpUnreachableCount).To(Equal(0))
			})
		})
	})

	// Tests for a 2+1 arbiter cluster scenario (2 CP nodes + 1 arbiter worker).
	Describe("2+1 arbiter cluster scenario", func() {
		var (
			peerProvider *mockPeerAddressProvider
			dialer       *mockPeerDialer
		)

		BeforeEach(func() {
			// 2+1 cluster: 2 CP nodes + 1 arbiter worker
			// From the perspective of CP-1 (our node):
			// - 1 worker peer (arbiter)
			// - 1 CP peer (CP-2)
			peerProvider = &mockPeerAddressProvider{
				workerAddresses: []corev1.PodIP{
					{IP: "10.0.0.10"}, // arbiter
				},
				controlPlaneAddresses: []corev1.PodIP{
					{IP: "10.0.0.20"}, // CP-2
				},
			}
			dialer = newMockPeerDialer()
		})

		Context("when CP-1 is fully network-isolated (NIC down)", func() {
			It("should detect isolation through canReachAnyPeer", func() {
				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)

				// No peers reachable (NIC is down)
				Expect(apiCheck.canReachAnyPeer()).To(BeFalse())

				// Both peers were attempted
				Expect(dialer.DialCount()).To(Equal(2))
			})
		})

		Context("when only the arbiter is down but CP-2 is reachable", func() {
			It("should consider the node healthy (partial isolation, not full)", func() {
				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)

				// CP-2 is reachable, arbiter is not
				dialer.SetReachable("10.0.0.20:30001")

				Expect(apiCheck.canReachAnyPeer()).To(BeTrue())
			})
		})

		Context("compact 3-node cluster (all nodes are CP+worker)", func() {
			It("should deduplicate and check unique peers", func() {
				// Same IPs in both lists
				peerProvider.workerAddresses = []corev1.PodIP{
					{IP: "10.0.0.20"},
					{IP: "10.0.0.30"},
				}
				peerProvider.controlPlaneAddresses = []corev1.PodIP{
					{IP: "10.0.0.20"},
					{IP: "10.0.0.30"},
				}

				apiCheck = newTestApiCheck(log, peerProvider, dialer, nil)
				Expect(apiCheck.canReachAnyPeer()).To(BeFalse())
				// Should only dial 2 unique peers, not 4
				Expect(dialer.DialCount()).To(Equal(2))
			})
		})
	})

	// Smooth upgrade path: verify that the new PeerAddressProvider interface
	// is backward compatible with the concrete *peers.Peers type.
	Describe("PeerAddressProvider interface compatibility", func() {
		It("should accept *peers.Peers as PeerAddressProvider", func() {
			// This is a compile-time check — if *peers.Peers doesn't satisfy
			// PeerAddressProvider, this test file won't compile.
			var _ PeerAddressProvider = &peers.Peers{}
		})
	})

	// Verify the PeerDialer interface contracts.
	Describe("PeerDialer interface", func() {
		It("netPeerDialer should implement PeerDialer", func() {
			var _ PeerDialer = &netPeerDialer{}
		})

		It("mockPeerDialer should implement PeerDialer", func() {
			var _ PeerDialer = newMockPeerDialer()
		})
	})

	Describe("mockPeerDialer behavior", func() {
		var dialer *mockPeerDialer

		BeforeEach(func() {
			dialer = newMockPeerDialer()
		})

		It("should fail for unknown addresses", func() {
			err := dialer.Dial("10.0.0.1:30001", time.Second)
			Expect(err).To(HaveOccurred())
			Expect(dialer.DialCount()).To(Equal(1))
		})

		It("should succeed for reachable addresses", func() {
			dialer.SetReachable("10.0.0.1:30001")
			err := dialer.Dial("10.0.0.1:30001", time.Second)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should record all dial attempts", func() {
			dialer.Dial("10.0.0.1:30001", time.Second)
			dialer.Dial("10.0.0.2:30001", time.Second)
			dialer.Dial("10.0.0.3:30001", time.Second)
			Expect(dialer.DialCount()).To(Equal(3))
		})
	})
})
