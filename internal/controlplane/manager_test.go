package controlplane

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Control-plane Manager", func() {
	Describe("preferred address types configuration", func() {
		AfterEach(func() {
			Expect(os.Unsetenv("PREFERRED_ADDRESS_TYPES")).To(Succeed())
		})

		It("should default to NodeName when the environment variable is not set", func() {
			manager := NewManager("node-1", nil)
			Expect(manager.preferredAddressTypes).To(Equal([]string{"NodeName"}))
		})

		It("should parse address types from the environment", func() {
			Expect(os.Setenv("PREFERRED_ADDRESS_TYPES", "InternalIP,NodeName")).To(Succeed())
			manager := NewManager("node-1", nil)
			Expect(manager.preferredAddressTypes).To(Equal([]string{"InternalIP", "NodeName"}))
		})
	})

	Describe("kubelet service check", func() {
		// RFC 6761 reserves .invalid, so resolution fails fast without network access
		const unreachableHost = "unreachable.invalid"

		var kubeletHost, kubeletPort string

		BeforeEach(func() {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			DeferCleanup(server.Close)

			serverUrl, err := url.Parse(server.URL)
			Expect(err).NotTo(HaveOccurred())
			kubeletHost = serverUrl.Hostname()
			kubeletPort = serverUrl.Port()
		})

		newTestManager := func(nodeName string, preferredAddressTypes []string, nodeAddresses []corev1.NodeAddress) *Manager {
			return &Manager{
				nodeName:              nodeName,
				preferredAddressTypes: preferredAddressTypes,
				nodeAddresses:         nodeAddresses,
				kubeletPort:           kubeletPort,
				log:                   ctrl.Log.WithName("controlPlane").WithName("Manager"),
			}
		}

		It("should contact the kubelet via the node name", func() {
			manager := newTestManager(kubeletHost, []string{"NodeName"}, nil)
			Expect(manager.isKubeletServiceRunning()).To(BeTrue())
		})

		It("should fail when the node name is not resolvable", func() {
			manager := newTestManager(unreachableHost, []string{"NodeName"}, nil)
			Expect(manager.isKubeletServiceRunning()).To(BeFalse())
		})

		It("should contact the kubelet via a node address", func() {
			addresses := []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: kubeletHost}}
			manager := newTestManager(unreachableHost, []string{"InternalIP"}, addresses)
			Expect(manager.isKubeletServiceRunning()).To(BeTrue())
		})

		It("should ignore addresses of other types", func() {
			addresses := []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: kubeletHost}}
			manager := newTestManager(unreachableHost, []string{"ExternalIP"}, addresses)
			Expect(manager.isKubeletServiceRunning()).To(BeFalse())
		})

		It("should fall back to later address types", func() {
			addresses := []corev1.NodeAddress{{Type: corev1.NodeInternalDNS, Address: unreachableHost}}
			manager := newTestManager(kubeletHost, []string{"InternalDNS", "NodeName"}, addresses)
			Expect(manager.isKubeletServiceRunning()).To(BeTrue())
		})

		It("should contact the kubelet via an IPv6 address", func() {
			listener, err := net.Listen("tcp", "[::1]:0")
			if err != nil {
				Skip("IPv6 loopback not available: " + err.Error())
			}

			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			Expect(server.Listener.Close()).To(Succeed())
			server.Listener = listener
			server.StartTLS()
			DeferCleanup(server.Close)

			serverUrl, err := url.Parse(server.URL)
			Expect(err).NotTo(HaveOccurred())

			addresses := []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: serverUrl.Hostname()}}
			manager := newTestManager(unreachableHost, []string{"InternalIP"}, addresses)
			manager.kubeletPort = serverUrl.Port()
			Expect(manager.isKubeletServiceRunning()).To(BeTrue())
		})

		It("should try all addresses of a type", func() {
			addresses := []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: unreachableHost},
				{Type: corev1.NodeInternalIP, Address: kubeletHost},
			}
			manager := newTestManager(unreachableHost, []string{"InternalIP"}, addresses)
			Expect(manager.isKubeletServiceRunning()).To(BeTrue())
		})
	})

	// These cover the helpers behind ensureEndpointNetworkPolicyUpToDate
	Describe("EndpointHealthCheckUrl NetworkPolicy exception", func() {
		port53 := intstr.FromInt32(53)
		portRule := networkingv1.NetworkPolicyEgressRule{
			Ports: []networkingv1.NetworkPolicyPort{{Port: &port53}},
		}
		podSelectorRule := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
		}
		cidrRuleA := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "203.0.113.5/32"}}},
		}
		cidrRuleB := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "203.0.113.6/32"}}},
		}

		Describe("isEndpointCIDRRule", func() {
			It("identifies a ports-only rule as not a CIDR rule", func() {
				Expect(isEndpointCIDRRule(portRule)).To(BeFalse())
			})
			It("identifies a podSelector-only rule as not a CIDR rule", func() {
				Expect(isEndpointCIDRRule(podSelectorRule)).To(BeFalse())
			})
			It("identifies a bare ipBlock rule as a CIDR rule", func() {
				Expect(isEndpointCIDRRule(cidrRuleA)).To(BeTrue())
			})
		})

		Describe("endpointCIDRsFromEgress", func() {
			It("extracts and sorts only the CIDR rules, ignoring everything else", func() {
				egress := []networkingv1.NetworkPolicyEgressRule{portRule, cidrRuleB, podSelectorRule, cidrRuleA}
				Expect(endpointCIDRsFromEgress(egress)).To(Equal([]string{"203.0.113.5/32", "203.0.113.6/32"}))
			})
			It("returns empty when there are no CIDR rules", func() {
				Expect(endpointCIDRsFromEgress([]networkingv1.NetworkPolicyEgressRule{portRule, podSelectorRule})).To(BeEmpty())
			})
		})

		Describe("replaceEndpointCIDRRules", func() {
			It("drops existing CIDR rules and appends fresh ones, preserving all other rules", func() {
				egress := []networkingv1.NetworkPolicyEgressRule{portRule, cidrRuleA, podSelectorRule}
				got := replaceEndpointCIDRRules(egress, []string{"198.51.100.1/32"})
				Expect(got).To(HaveLen(3))
				Expect(got).To(ContainElement(portRule))
				Expect(got).To(ContainElement(podSelectorRule))
				Expect(endpointCIDRsFromEgress(got)).To(Equal([]string{"198.51.100.1/32"}))
			})
			It("removes all CIDR rules and adds none when cidrs is empty (fail-closed)", func() {
				egress := []networkingv1.NetworkPolicyEgressRule{portRule, cidrRuleA}
				got := replaceEndpointCIDRRules(egress, nil)
				Expect(got).To(Equal([]networkingv1.NetworkPolicyEgressRule{portRule}))
			})
		})

		Describe("sameCIDRs", func() {
			It("is true for equal slices", func() {
				Expect(sameCIDRs([]string{"a", "b"}, []string{"a", "b"})).To(BeTrue())
			})
			It("is false for different lengths", func() {
				Expect(sameCIDRs([]string{"a"}, []string{"a", "b"})).To(BeFalse())
			})
			It("is false for different contents", func() {
				Expect(sameCIDRs([]string{"a", "b"}, []string{"a", "c"})).To(BeFalse())
			})
			It("is true for two empty/nil slices", func() {
				Expect(sameCIDRs(nil, []string{})).To(BeTrue())
			})
		})
	})
})
