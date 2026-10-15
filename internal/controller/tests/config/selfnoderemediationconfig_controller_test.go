package testconfig

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/v5/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/v5/internal/controller/tests/shared"
	"github.com/medik8s/self-node-remediation/v5/internal/utils"
)

// Without sorting, this can reorder the rendered NetworkPolicy's
// egress rules, which looks like a real change to apply.ApplyObject
// and triggers an unnecessary update on every recheck.
func TestIpsToSortedCIDRsIsOrderIndependent(t *testing.T) {
	ipv4A := net.ParseIP("203.0.113.10")
	ipv4B := net.ParseIP("203.0.113.11")
	ipv4C := net.ParseIP("203.0.113.12")
	ipv6A := net.ParseIP("2001:db8::1")
	ipv6B := net.ParseIP("2001:db8::2")

	orderings := [][]net.IP{
		{ipv4A, ipv4B, ipv4C, ipv6A, ipv6B},
		{ipv4C, ipv4A, ipv6B, ipv4B, ipv6A},
		{ipv6B, ipv6A, ipv4C, ipv4B, ipv4A},
	}

	var first []string
	for i, ips := range orderings {
		got := utils.IpsToSortedCIDRs(ips)
		if i == 0 {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("ordering %d: got %d CIDRs, want %d (same set, different input order): %v vs %v", i, len(got), len(first), got, first)
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("ordering %d: result not order-independent at index %d: got %q, want %q (full: %v vs %v)", i, j, got[j], first[j], got, first)
			}
		}
	}
}

func TestIpsToSortedCIDRsFormatsIPv4As32AndIPv6As128(t *testing.T) {
	got := utils.IpsToSortedCIDRs([]net.IP{
		net.ParseIP("203.0.113.5"),
		net.ParseIP("2001:db8::1"),
	})
	want := []string{"2001:db8::1/128", "203.0.113.5/32"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestIpsToSortedCIDRsEmptyInput(t *testing.T) {
	got := utils.IpsToSortedCIDRs(nil)
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

const unresolvableURL = "definitely-not-a-real-host.invalid"

func TestResolveEndpointCIDRsWithFallbackFailsClosedWithNoPriorCache(t *testing.T) {
	cidrs, newURL, newCIDRs, err := utils.ResolveEndpointCIDRsWithFallback(unresolvableURL, "", nil)
	if err == nil {
		t.Fatalf("expected a resolution error for %q, got none", unresolvableURL)
	}
	if len(cidrs) != 0 {
		t.Fatalf("expected no CIDRs on a first-ever resolution failure, got %v", cidrs)
	}
	if newURL != "" || len(newCIDRs) != 0 {
		t.Fatalf("expected the (empty) cache to be left unchanged, got url=%q cidrs=%v", newURL, newCIDRs)
	}
}

func TestResolveEndpointCIDRsWithFallbackReusesCacheOnTransientFailureForSameURL(t *testing.T) {
	cachedURL := unresolvableURL
	cachedCIDRs := []string{"203.0.113.5/32"}

	cidrs, newURL, newCIDRs, err := utils.ResolveEndpointCIDRsWithFallback(unresolvableURL, cachedURL, cachedCIDRs)
	if err == nil {
		t.Fatalf("expected a resolution error for %q, got none", unresolvableURL)
	}
	if len(cidrs) != 1 || cidrs[0] != "203.0.113.5/32" {
		t.Fatalf("expected the previously resolved CIDRs to be reused on a transient failure, got %v", cidrs)
	}
	if newURL != cachedURL || len(newCIDRs) != 1 || newCIDRs[0] != "203.0.113.5/32" {
		t.Fatalf("expected the cache to be left unchanged, got url=%q cidrs=%v", newURL, newCIDRs)
	}
}

func TestResolveEndpointCIDRsWithFallbackFailsClosedWhenURLChanged(t *testing.T) {
	cachedURL := "203.0.113.5"
	cachedCIDRs := []string{"203.0.113.5/32"}

	cidrs, newURL, newCIDRs, err := utils.ResolveEndpointCIDRsWithFallback(unresolvableURL, cachedURL, cachedCIDRs)
	if err == nil {
		t.Fatalf("expected a resolution error for %q, got none", unresolvableURL)
	}
	if len(cidrs) != 0 {
		t.Fatalf("expected no CIDRs when the failing URL doesn't match the cached URL, got %v", cidrs)
	}

	if newURL != cachedURL || len(newCIDRs) != 1 || newCIDRs[0] != "203.0.113.5/32" {
		t.Fatalf("expected the stale cache to be left unchanged, got url=%q cidrs=%v", newURL, newCIDRs)
	}
}

func TestResolveEndpointCIDRsWithFallbackUpdatesCacheOnSuccess(t *testing.T) {
	const testIP = "203.0.113.5"
	cidrs, newURL, newCIDRs, err := utils.ResolveEndpointCIDRsWithFallback(testIP, "", nil)
	if err != nil {
		t.Fatalf("unexpected resolution error for IP literal %q: %v", testIP, err)
	}
	if len(cidrs) != 1 || cidrs[0] != testIP+"/32" {
		t.Fatalf("got %v, want [%s/32]", cidrs, testIP)
	}
	if newURL != testIP || len(newCIDRs) != 1 || newCIDRs[0] != testIP+"/32" {
		t.Fatalf("expected the cache to be updated to the new resolution, got url=%q cidrs=%v", newURL, newCIDRs)
	}
}

// An empty EndpointHealthCheckUrl (field unset/cleared) resolves successfully
// to no CIDRs, which must also clear any stale cached CIDRs from a
// previously-configured URL
func TestResolveEndpointCIDRsWithFallbackClearsCacheWhenURLCleared(t *testing.T) {
	cachedURL := "203.0.113.5"
	cachedCIDRs := []string{"203.0.113.5/32"}

	cidrs, newURL, newCIDRs, err := utils.ResolveEndpointCIDRsWithFallback("", cachedURL, cachedCIDRs)
	if err != nil {
		t.Fatalf("unexpected error resolving an empty URL: %v", err)
	}
	if len(cidrs) != 0 {
		t.Fatalf("expected no CIDRs for an empty URL, got %v", cidrs)
	}
	if newURL != "" || len(newCIDRs) != 0 {
		t.Fatalf("expected the cache to be cleared, got url=%q cidrs=%v", newURL, newCIDRs)
	}
}

var _ = Describe("SNR Config Test", func() {
	dsName := "self-node-remediation-ds"
	networkPolicyName := "self-node-remediation-ds-np"
	var config *selfnoderemediationv1alpha1.SelfNodeRemediationConfig
	var ds *appsv1.DaemonSet
	dsKey := types.NamespacedName{
		Namespace: shared.Namespace,
		Name:      dsName,
	}
	networkPolicyKey := types.NamespacedName{
		Namespace: shared.Namespace,
		Name:      networkPolicyName,
	}
	BeforeEach(func() {
		ds = &appsv1.DaemonSet{}
		config = shared.GenerateTestConfig()
	})

	AfterEach(func() {
		tmpConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
		tmpDs := &appsv1.DaemonSet{}
		tmpNetworkPolicy := &networkingv1.NetworkPolicy{}
		//verify config exist
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(config), tmpConfig)).To(Succeed())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		//ds may take quite a while to create (for example with a 5 sec timeout this test usually fails locally)
		timeToWaitForDSToCreate := 25 * time.Second
		//verify ds exist
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(context.Background(), dsKey, tmpDs)).To(Succeed())
		}, timeToWaitForDSToCreate, 250*time.Millisecond).Should(Succeed())

		Expect(k8sClient.Delete(context.TODO(), tmpConfig)).To(Succeed())
		Eventually(func(g Gomega) {
			err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(config), &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		Expect(k8sClient.Delete(context.TODO(), tmpDs)).To(Succeed())
		Eventually(func(g Gomega) {
			err := k8sClient.Get(context.Background(), dsKey, &appsv1.DaemonSet{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		//NetworkPolicy is best-effort here: some specs intentionally pre-create a DaemonSet with an
		//incompatible selector, which makes every sync of the DS+NetworkPolicy
		//pair fail before the NetworkPolicy is ever created. Clean it up if it exists,
		//but don't require it, since the dedicated "NetworkPolicy should be created
		//alongside the DaemonSet" test already asserts its creation
		if err := k8sClient.Get(context.Background(), networkPolicyKey, tmpNetworkPolicy); err == nil {
			Expect(k8sClient.Delete(context.TODO(), tmpNetworkPolicy)).To(Succeed())
			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), networkPolicyKey, &networkingv1.NetworkPolicy{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	Context("DS installation", func() {
		BeforeEach(func() {
			config.Spec.WatchdogFilePath = "/dev/foo"
			config.Spec.SafeTimeToAssumeNodeRebootedSeconds = pointer.Int(123)
			config.Spec.HostPort = 30111
		})

		JustBeforeEach(func() {
			//Creates config which in turn will create the DS
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())

		})

		It("Config CR should be created", func() {
			Expect(k8sClient).To(Not(BeNil()))

			createdConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal(config.Spec.WatchdogFilePath))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(Equal(config.Spec.SafeTimeToAssumeNodeRebootedSeconds))
			Expect(createdConfig.Spec.HostPort).To(Equal(30111))
		})

		It("Cert Secret should be created", func() {
			Eventually(func(g Gomega) {
				_, _, _, err := certReader.GetCerts()
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).To(Succeed())
			}, 15*time.Second, 250*time.Millisecond).Should(Succeed())

		})

		It("Daemonset should be created", func() {
			Eventually(func() error {
				return k8sClient.Get(context.Background(), dsKey, ds)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			dsContainers := ds.Spec.Template.Spec.Containers
			Expect(len(dsContainers)).To(BeNumerically("==", 1))
			container := dsContainers[0]
			Expect(container.Image).To(Equal(shared.DsDummyImageName))
			envVars := getEnvVarMap(container.Env)
			Expect(envVars["WATCHDOG_PATH"].Value).To(Equal(config.Spec.WatchdogFilePath))
			Expect(envVars["PREFERRED_ADDRESS_TYPES"].Value).To(Equal(""))
			Expect(envVars).To(HaveKey("PEER_TOPOLOGY_KEY"))
			Expect(envVars["PEER_TOPOLOGY_KEY"].Value).To(BeEmpty())

			Expect(len(ds.OwnerReferences)).To(Equal(1))
			Expect(ds.OwnerReferences[0].Name).To(Equal(config.Name))
			Expect(ds.OwnerReferences[0].Kind).To(Equal("SelfNodeRemediationConfig"))
			Expect(len(container.Ports)).To(BeNumerically("==", 1))
			port := container.Ports[0]
			Expect(port.ContainerPort).To(BeEquivalentTo(30111))
			Expect(port.HostPort).To(BeZero())

			Expect(container.SecurityContext).ToNot(BeNil())
			Expect(container.SecurityContext.Privileged).To(Equal(pointer.Bool(true)))
			Expect(container.SecurityContext.ReadOnlyRootFilesystem).To(Equal(pointer.Bool(true)))

			Expect(container.ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))

			Expect(container.LivenessProbe).ToNot(BeNil())
			Expect(container.LivenessProbe.HTTPGet.Path).To(Equal("/healthz"))
			Expect(container.LivenessProbe.HTTPGet.Port.IntValue()).To(Equal(8081))

			Expect(container.ReadinessProbe).ToNot(BeNil())
			Expect(container.ReadinessProbe.HTTPGet.Path).To(Equal("/readyz"))
			Expect(container.ReadinessProbe.HTTPGet.Port.IntValue()).To(Equal(8081))

			Expect(container.StartupProbe).ToNot(BeNil())
			Expect(container.StartupProbe.HTTPGet.Path).To(Equal("/healthz"))
			Expect(container.StartupProbe.HTTPGet.Port.IntValue()).To(Equal(8081))
			Expect(container.StartupProbe.FailureThreshold).To(BeEquivalentTo(12))

			Expect(container.TerminationMessagePolicy).To(Equal(corev1.TerminationMessageFallbackToLogsOnError))
		})
		When("Configuration has customized address types", func() {
			BeforeEach(func() {
				config.Spec.PreferredAddressTypes = []string{"InternalDNS", "InternalIP"}
			})
			It("Daemonset should have comma-separated address types in env var", func() {
				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					dsContainers := ds.Spec.Template.Spec.Containers
					g.Expect(len(dsContainers)).To(BeNumerically("==", 1))
					container := dsContainers[0]
					envVars := getEnvVarMap(container.Env)
					g.Expect(envVars["PREFERRED_ADDRESS_TYPES"].Value).To(Equal("InternalDNS,InternalIP"))
				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
			})
		})

		It("NetworkPolicy should be created alongside the DaemonSet", func() {
			np := &networkingv1.NetworkPolicy{}
			Eventually(func() error {
				return k8sClient.Get(context.Background(), networkPolicyKey, np)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

			By("owning the NetworkPolicy via the config CR")
			Expect(len(np.OwnerReferences)).To(Equal(1))
			Expect(np.OwnerReferences[0].Name).To(Equal(config.Name))
			Expect(np.OwnerReferences[0].Kind).To(Equal("SelfNodeRemediationConfig"))

			By("selecting only the agent pods")
			Expect(np.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{
				"app.kubernetes.io/name":      "self-node-remediation",
				"app.kubernetes.io/component": "agent",
			}))

			By("restricting both ingress and egress")
			Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))

			By("allowing ingress from peer agent pods on the configured hostPort only")
			Expect(np.Spec.Ingress).To(HaveLen(1))
			ingressRule := np.Spec.Ingress[0]
			Expect(ingressRule.From).To(HaveLen(1))
			Expect(ingressRule.From[0].PodSelector.MatchLabels).To(Equal(map[string]string{
				"app.kubernetes.io/name":      "self-node-remediation",
				"app.kubernetes.io/component": "agent",
			}))
			Expect(ingressRule.Ports).To(HaveLen(1))
			Expect(ingressRule.Ports[0].Port.IntValue()).To(Equal(config.Spec.HostPort))
			Expect(*ingressRule.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))

			By("having exactly 4 egress rules: peer, DNS, HTTPS, kubelet")
			Expect(np.Spec.Egress).To(HaveLen(4))

			By("allowing egress to peer agent pods on the configured hostPort")
			foundPeerEgressRule := false
			for _, egressRule := range np.Spec.Egress {
				if len(egressRule.To) == 1 && egressRule.To[0].PodSelector != nil {
					foundPeerEgressRule = true
					Expect(egressRule.To[0].PodSelector.MatchLabels).To(Equal(map[string]string{
						"app.kubernetes.io/name":      "self-node-remediation",
						"app.kubernetes.io/component": "agent",
					}))
					Expect(egressRule.Ports).To(HaveLen(1))
					Expect(egressRule.Ports[0].Port.IntValue()).To(Equal(config.Spec.HostPort))
				}
			}
			Expect(foundPeerEgressRule).To(BeTrue())

			By("allowing DNS resolution on ports 53 and 5353 (OVN-Kubernetes Service->pod DNAT target) for UDP and TCP")
			foundDNSEgressRule := false
			for _, egressRule := range np.Spec.Egress {
				hasUDP := false
				for _, p := range egressRule.Ports {
					if *p.Protocol == corev1.ProtocolUDP {
						hasUDP = true
					}
				}
				if len(egressRule.To) == 0 && hasUDP {
					foundDNSEgressRule = true
					ports := map[corev1.Protocol][]int32{}
					for _, p := range egressRule.Ports {
						ports[*p.Protocol] = append(ports[*p.Protocol], p.Port.IntVal)
					}
					Expect(ports).To(Equal(map[corev1.Protocol][]int32{
						corev1.ProtocolUDP: {53, 5353},
						corev1.ProtocolTCP: {53, 5353},
					}))
				}
			}
			Expect(foundDNSEgressRule).To(BeTrue())

			By("allowing HTTPS egress on ports 443 and 6443")
			foundHTTPSEgressRule := false
			for _, egressRule := range np.Spec.Egress {
				if len(egressRule.To) == 0 && len(egressRule.Ports) == 2 && *egressRule.Ports[0].Protocol == corev1.ProtocolTCP {
					foundHTTPSEgressRule = true
					ports := map[corev1.Protocol][]int32{}
					for _, p := range egressRule.Ports {
						ports[*p.Protocol] = append(ports[*p.Protocol], p.Port.IntVal)
					}
					Expect(ports).To(Equal(map[corev1.Protocol][]int32{
						corev1.ProtocolTCP: {443, 6443},
					}))
				}
			}
			Expect(foundHTTPSEgressRule).To(BeTrue())

			By("allowing the own-node kubelet check on port 10250 (isKubeletServiceRunningOnAddress)")
			foundKubeletEgressRule := false
			for _, egressRule := range np.Spec.Egress {
				if len(egressRule.To) == 0 && len(egressRule.Ports) == 1 && egressRule.Ports[0].Port.IntValue() == 10250 {
					foundKubeletEgressRule = true
					Expect(*egressRule.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))
				}
			}
			Expect(foundKubeletEgressRule).To(BeTrue())
		})

		It("should recreate the NetworkPolicy if it is deleted", func() {
			np := &networkingv1.NetworkPolicy{}
			Eventually(func() error {
				return k8sClient.Get(context.Background(), networkPolicyKey, np)
			}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
			originalUID := np.UID

			Expect(k8sClient.Delete(context.Background(), np)).To(Succeed())

			Eventually(func(g Gomega) {
				recreated := &networkingv1.NetworkPolicy{}
				g.Expect(k8sClient.Get(context.Background(), networkPolicyKey, recreated)).To(Succeed())
				g.Expect(recreated.UID).NotTo(Equal(originalUID))
			}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
		})
		When("Configuration has EndpointHealthCheckUrl set to a resolvable address", func() {
			// Use an IP literal rather than a hostname: net.LookupIP
			// resolves IP-literal strings immediately without a DNS lookup, so this
			// keeps the test deterministic and network-independent, while still
			// exercising the same resolution code path production hostnames use.
			const testIP = "203.0.113.5"
			BeforeEach(func() {
				config.Spec.EndpointHealthCheckUrl = testIP
			})
			It("NetworkPolicy should include a 5th egress rule scoped to that IP via ipBlock", func() {
				np := &networkingv1.NetworkPolicy{}
				Eventually(func() error {
					return k8sClient.Get(context.Background(), networkPolicyKey, np)
				}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

				By("having a 5th egress rule scoped to the resolved IP, not allowing all egress")
				Expect(np.Spec.Egress).To(HaveLen(5))

				foundScopedRule := false
				foundAllowAllRule := false
				for _, egressRule := range np.Spec.Egress {
					if len(egressRule.To) == 0 && len(egressRule.Ports) == 0 {
						foundAllowAllRule = true
					}
					if len(egressRule.To) == 1 && egressRule.To[0].IPBlock != nil {
						foundScopedRule = true
						Expect(egressRule.To[0].IPBlock.CIDR).To(Equal(testIP + "/32"))
						Expect(egressRule.Ports).To(BeEmpty())
					}
				}
				Expect(foundScopedRule).To(BeTrue())
				Expect(foundAllowAllRule).To(BeFalse())
			})
		})
		When("Configuration has EndpointHealthCheckUrl set to an address that can't be resolved", func() {
			BeforeEach(func() {
				config.Spec.EndpointHealthCheckUrl = unresolvableURL
			})
			It("should fail closed: add no egress exception rather than allowing all egress", func() {
				np := &networkingv1.NetworkPolicy{}
				Eventually(func() error {
					return k8sClient.Get(context.Background(), networkPolicyKey, np)
				}, 10*time.Second, 250*time.Millisecond).Should(BeNil())

				By("keeping only the original 4 egress rules: peer, DNS, HTTPS, kubelet")
				Expect(np.Spec.Egress).To(HaveLen(4))

				for _, egressRule := range np.Spec.Egress {
					Expect(len(egressRule.To) == 0 && len(egressRule.Ports) == 0).To(BeFalse(),
						"should never fall back to an unrestricted allow-all egress rule")
				}
			})
		})
		When("Configuration has a peer topology key", func() {
			BeforeEach(func() {
				config.Spec.PeerTopologyKey = corev1.LabelTopologyZone
			})
			It("Daemonset should pass the key to the agents in an env var", func() {
				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					envVars := getEnvVarMap(ds.Spec.Template.Spec.Containers[0].Env)
					g.Expect(envVars["PEER_TOPOLOGY_KEY"].Value).To(Equal(corev1.LabelTopologyZone))
				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

				// a later change of the configuration reaches the agents too
				config.Spec.PeerTopologyKey = ""
				Expect(k8sClient.Update(context.Background(), config)).To(Succeed())
				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					envVars := getEnvVarMap(ds.Spec.Template.Spec.Containers[0].Env)
					g.Expect(envVars["PEER_TOPOLOGY_KEY"].Value).To(BeEmpty())
				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
			})
		})
		When("Configuration has customized tolerations", func() {
			var expectedToleration corev1.Toleration
			BeforeEach(func() {
				expectedToleration = corev1.Toleration{Key: "dummyTolerationKey", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule}
				config.Spec.CustomDsTolerations = []corev1.Toleration{expectedToleration}
			})
			It("Daemonset should have customized tolerations", func() {
				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					g.Expect(ds.Spec.Template.Spec.Tolerations).ToNot(BeNil())
					g.Expect(len(ds.Spec.Template.Spec.Tolerations)).To(Equal(4))

					//Verify customized toleration found
					g.Expect(ds.Spec.Template.Spec.Tolerations).To(ContainElement(expectedToleration))

				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

				//update configuration
				config.Spec.PeerUpdateInterval = &metav1.Duration{Duration: time.Second * 10}
				Expect(k8sClient.Update(context.Background(), config)).To(Succeed())

				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					//verify ds has new configuration
					envVars := getEnvVarMap(ds.Spec.Template.Spec.Containers[0].Env)
					g.Expect(envVars["PEER_UPDATE_INTERVAL"].Value).To(Equal(strconv.Itoa(int(time.Second * 10))))
					//verify toleration remains on ds

					g.Expect(ds.Spec.Template.Spec.Tolerations).ToNot(BeNil())
					g.Expect(len(ds.Spec.Template.Spec.Tolerations)).To(Equal(4))

					//Verify customized toleration found
					g.Expect(ds.Spec.Template.Spec.Tolerations).To(ContainElement(expectedToleration))
				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
			})
		})
		When("Configuration has customized nodeAffinity node selector", func() {
			var defaultNodeSelectorRequirement, customNodeSelectorRequirement corev1.NodeSelectorRequirement
			BeforeEach(func() {
				defaultNodeSelectorRequirement = corev1.NodeSelectorRequirement{Key: "remediation.medik8s.io/exclude-from-remediation", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"true"}}
				customNodeSelectorRequirement = corev1.NodeSelectorRequirement{Key: "dummyLabel", Operator: corev1.NodeSelectorOpIn, Values: []string{"true"}}
				config.Spec.CustomDsNodeSelectorRequirements = []corev1.NodeSelectorRequirement{customNodeSelectorRequirement}
			})
			It("Daemonset should have customized nodeAffinity node selector", func() {
				verifyNodeSelectorTerms := func(g Gomega) {
					matchExpressions := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
					// Exactly default + custom, no duplicates
					g.Expect(matchExpressions).To(ConsistOf(defaultNodeSelectorRequirement, customNodeSelectorRequirement))
				}

				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					nodeSelectorTerms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
					g.Expect(nodeSelectorTerms).To(HaveLen(1))

					// Default node selector must always be present when custom is specified
					verifyNodeSelectorTerms(g)
				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

				//update configuration
				config.Spec.PeerUpdateInterval = &metav1.Duration{Duration: time.Second * 10}
				Expect(k8sClient.Update(context.Background(), config)).To(Succeed())

				Eventually(func(g Gomega) {
					ds = &appsv1.DaemonSet{}
					g.Expect(k8sClient.Get(context.Background(), dsKey, ds)).Should(BeNil())

					//verify ds has new configuration
					envVars := getEnvVarMap(ds.Spec.Template.Spec.Containers[0].Env)
					g.Expect(envVars["PEER_UPDATE_INTERVAL"].Value).To(Equal(strconv.Itoa(int(time.Second * 10))))

					//verify node selectors remain on ds after config update
					nodeSelectorTerms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
					g.Expect(nodeSelectorTerms).To(HaveLen(1))

					// Default node selector must always be present when custom is specified
					verifyNodeSelectorTerms(g)
				}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
			})
		})
		Context("DS Recreation on Operator Update", func() {
			var timeToWaitForDsUpdate = 6 * time.Second
			var oldDsVersion, currentDsVersion = "0", "1"
			createInitialDs := func() {
				EventuallyWithOffset(1, func() error {
					return k8sClient.Create(context.Background(), ds)
				}, 2*time.Second, 250*time.Millisecond).Should(Succeed())

				Eventually(func() error {
					return k8sClient.Get(context.Background(), dsKey, ds)
				}, 2*time.Second, 250*time.Millisecond).Should(BeNil())
			}
			When("ds version has not changed", func() {
				BeforeEach(func() {
					ds = generateDs(dsName, shared.Namespace, currentDsVersion)
					//Ds needs to be created before the config,
					//because otherwise the  config creation will also trigger a DS create which will cause a race condition
					createInitialDs()
				})
				It("Daemonset should not recreated", func() {
					//Wait to make sure DS isn't recreated
					time.Sleep(timeToWaitForDsUpdate)
					Expect(k8sClient.Get(context.Background(), dsKey, ds)).To(BeNil())
					Expect(ds.Annotations["original-ds"]).To(Equal("true"))
				})
			})

			When("ds version has changed", func() {
				BeforeEach(func() {
					//creating an DS with old version
					ds = generateDs(dsName, shared.Namespace, oldDsVersion)
					//Ds needs to be created before the config,
					//because otherwise the  config creation will also trigger a DS create which will cause a race condition
					createInitialDs()
				})
				It("Daemonset should be recreated", func() {
					//Wait until DS is recreated
					Eventually(func() bool {
						if err := k8sClient.Get(context.Background(), dsKey, ds); err == nil {
							return ds.Annotations["snr.medik8s.io/force-deletion-revision"] == currentDsVersion
						}
						return false
					}, timeToWaitForDsUpdate, 250*time.Millisecond).Should(BeTrue())
					Expect(ds.Annotations["original-ds"]).To(Equal(""))

				})
			})
		})
	})

	Context("SNRC defaults", func() {
		JustBeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})

		It("Config CR should be created with default values", func() {
			createdConfig := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
			configKey := client.ObjectKeyFromObject(config)

			Eventually(func() error {
				return k8sClient.Get(context.Background(), configKey, createdConfig)
			}, 5*time.Second, 250*time.Millisecond).Should(BeNil())

			Expect(createdConfig.Spec.WatchdogFilePath).To(Equal("/dev/watchdog"))
			Expect(createdConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds).To(BeNil())
			Expect(createdConfig.Spec.MaxApiErrorThreshold).To(Equal(3))

			Expect(createdConfig.Spec.PeerApiServerTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.PeerRequestTimeout.Seconds()).To(BeEquivalentTo(7))
			Expect(createdConfig.Spec.PeerDialTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.ApiServerTimeout.Seconds()).To(BeEquivalentTo(5))
			Expect(createdConfig.Spec.ApiCheckInterval.Seconds()).To(BeEquivalentTo(15))
			Expect(createdConfig.Spec.PeerUpdateInterval.Seconds()).To(BeEquivalentTo(15 * 60))
			Expect(createdConfig.Spec.MinPeersForRemediation).To(BeEquivalentTo(1))
		})
	})

	Context("Wrong self node remediation config CR", func() {
		var dsResourceVersion string
		var key types.NamespacedName
		BeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
			//allow ds to be created
			time.Sleep(time.Second)

			ds = &appsv1.DaemonSet{}
			key = types.NamespacedName{
				Namespace: shared.Namespace,
				Name:      dsName,
			}

			By("get DS resource version")
			Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
			dsResourceVersion = ds.ResourceVersion

			By("create a config CR")
			config := shared.GenerateTestConfig()
			config.Name = "not-the-expected-name"
			config.Spec.WatchdogFilePath = "foo"
			config.Spec.SafeTimeToAssumeNodeRebootedSeconds = pointer.Int(9999)

			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})

		It("Daemonset should not be changed", func() {
			Consistently(func() string {
				Expect(k8sClient.Get(context.Background(), key, ds)).To(Succeed())
				return ds.ResourceVersion
			}, 20*time.Second, 1*time.Second).Should(Equal(dsResourceVersion))
		})
	})

	Context("Config is created when SNR CR already exists", func() {
		var snr *selfnoderemediationv1alpha1.SelfNodeRemediation

		BeforeEach(func() {
			snr = &selfnoderemediationv1alpha1.SelfNodeRemediation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      shared.UnhealthyNodeName,
					Namespace: shared.Namespace,
				},
			}
			createSNR(snr)
			//cleanup snr
			DeferCleanup(func() {
				Expect(k8sClient.Delete(context.Background(), snr)).To(Succeed())
				Eventually(func(g Gomega) bool {
					err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), &selfnoderemediationv1alpha1.SelfNodeRemediation{})
					return apierrors.IsNotFound(err)
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
			})
			//simulating manager
			setSNRStatusDisabled(snr)
			//Verify status is persisted before continuing
			shared.VerifySNRStatusExist(k8sClient, snr, "Disabled", metav1.ConditionTrue)
		})

		JustBeforeEach(func() {
			Expect(k8sClient.Create(context.Background(), config)).To(Succeed())
		})
		It("SNR disabled Condition status should be removed", func() {
			verifySNRStatusRemoved(snr, "Disabled")

		})
	})

})

func getEnvVarMap(vars []corev1.EnvVar) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, envVar := range vars {
		m[envVar.Name] = envVar
	}
	return m
}

func generateDs(name, namespace, version string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{"snr.medik8s.io/force-deletion-revision": version, "original-ds": "true"},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "example",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "example",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "example-container",
							Image: "busybox",
							Command: []string{
								"/bin/sh",
								"-c",
								"while true; do echo hello; sleep 10;done",
							},
						},
					},
				},
			},
		},
	}
}

func verifySNRStatusRemoved(snr *selfnoderemediationv1alpha1.SelfNodeRemediation, statusType string) {
	Eventually(func(g Gomega) {
		tmpSNR := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), tmpSNR)).To(Succeed())
		g.Expect(meta.FindStatusCondition(tmpSNR.Status.Conditions, statusType)).To(BeNil())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
}

func createSNR(snr *selfnoderemediationv1alpha1.SelfNodeRemediation) {
	Expect(k8sClient.Create(context.Background(), snr)).To(Succeed())
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), &selfnoderemediationv1alpha1.SelfNodeRemediation{})).To(Succeed())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

}

func setSNRStatusDisabled(snr *selfnoderemediationv1alpha1.SelfNodeRemediation) {
	snrTmp := &selfnoderemediationv1alpha1.SelfNodeRemediation{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), snrTmp)).To(Succeed())
		snrTmp.Status.Conditions = []metav1.Condition{metav1.Condition{
			Type:               "Disabled",
			Status:             metav1.ConditionTrue,
			Reason:             "SNRDisabledConfigurationNotFound",
			LastTransitionTime: metav1.Time{Time: time.Now()},
		}}
		g.Expect(k8sClient.Status().Update(context.Background(), snrTmp)).To(Succeed())
	}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
}
