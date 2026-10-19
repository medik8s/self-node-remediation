package controlplane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-ping/ping"
	"github.com/medik8s/common/pkg/nodes"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/v5/internal/certificates"
	"github.com/medik8s/self-node-remediation/v5/internal/peers"
	"github.com/medik8s/self-node-remediation/v5/internal/utils"
)

const (
	defaultKubeletPort        = "10250"
	nodeNameAddressType       = "NodeName"
	endpointNetworkPolicyName = "self-node-remediation-ds-np"

	// how long isEndpointAccessible waits for a
	// NetworkPolicy patch to actually take effect
	endpointAccessRetriesAfterNetworkPolicyPatch = 5
	endpointAccessRetryBackoff                   = 2 * time.Second
)

// Manager contains logic and info needed to fence and remediate controlplane nodes
type Manager struct {
	nodeName                     string
	namespace                    string
	nodeRole                     peers.Role
	preferredAddressTypes        []string
	nodeAddresses                []corev1.NodeAddress
	kubeletPort                  string
	endpointHealthCheckUrl       string
	wasEndpointAccessibleAtStart bool
	client                       client.Client
	log                          logr.Logger
}

// NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) *Manager {
	var preferredAddressTypes []string
	rawPreferredAddressTypes := os.Getenv("PREFERRED_ADDRESS_TYPES")
	if rawPreferredAddressTypes != "" {
		preferredAddressTypes = strings.Split(rawPreferredAddressTypes, ",")
	} else {
		preferredAddressTypes = []string{nodeNameAddressType}
	}
	for i, addressType := range preferredAddressTypes {
		preferredAddressTypes[i] = strings.TrimSpace(addressType)
	}

	log := ctrl.Log.WithName("controlPlane").WithName("Manager")
	namespace, err := utils.GetDeploymentNamespace()
	if err != nil {
		log.Error(err, "could not determine deployment namespace; will not keep the EndpointHealthCheckUrl NetworkPolicy exception up to date before pinging")
	}

	return &Manager{
		nodeName:                     nodeName,
		namespace:                    namespace,
		endpointHealthCheckUrl:       os.Getenv("END_POINT_HEALTH_CHECK_URL"),
		preferredAddressTypes:        preferredAddressTypes,
		kubeletPort:                  defaultKubeletPort,
		client:                       myClient,
		wasEndpointAccessibleAtStart: false,
		log:                          log,
	}
}

func (manager *Manager) Start(_ context.Context) error {
	if err := manager.initializeManager(); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) IsControlPlane() bool {
	return manager.nodeRole == peers.ControlPlane
}

func (manager *Manager) IsControlPlaneHealthy(workerPeerResponse peers.Response, canOtherControlPlanesBeReached bool) bool {
	switch workerPeerResponse.Reason {
	//reported unhealthy by worker peers
	case peers.UnHealthyBecausePeersResponse:
		manager.log.Info("We are deciding the control plane is not healthy because the peer response was UnHealthyBecausePeersResponse")
		return false
	case peers.UnHealthyBecauseNodeIsIsolated:
		manager.log.Info("While trying to determine if the control plane is healthy, the peer response was "+
			"UnHealthyBecauseNodeIsIsolated, so we are returning true if we could reach other control plane nodes",
			"canOtherControlPlanesBeReached", canOtherControlPlanesBeReached)
		return canOtherControlPlanesBeReached
	//reported healthy by worker peers
	case peers.HealthyBecauseErrorsThresholdNotReached, peers.HealthyBecauseCRNotFound, peers.HealthyBecauseNoPeersResponseNotReachedTimeout:
		manager.log.Info("We are deciding that the control plane is healthy because either: "+
			"HealthyBecauseErrorsThresholdNotReached "+
			", HealthyBecauseCRNotFound,  or HealthyBecauseNoPeersResponseNotReachedTimeout",
			"reason", workerPeerResponse.Reason)
		return true
	//controlPlane node has connection to most workers, we assume it's not isolated (or at least that the controlPlane node that does not have worker peers quorum will reboot)
	case peers.HealthyBecauseMostPeersCantAccessAPIServer:
		didDiagnosticsPass := manager.isDiagnosticsPassed()
		manager.log.Info("The peers couldn't access the API server, so we are returning whether "+
			"diagnostics passed", "didDiagnosticsPass", didDiagnosticsPass)
		return didDiagnosticsPass
	case peers.HealthyBecauseNoPeersWereFound:
		didDiagnosticsPass := manager.isDiagnosticsPassed()

		manager.log.Info("We couldn't find any peers so we are returning didDiagnosticsPass && "+
			"canOtherControlPlanesBeReached", "didDiagnosticsPass", didDiagnosticsPass,
			"canOtherControlPlanesBeReached", canOtherControlPlanesBeReached)

		return didDiagnosticsPass && canOtherControlPlanesBeReached

	default:
		errorText := "node is considered unhealthy by worker peers for an unknown reason"
		manager.log.Error(errors.New(errorText), errorText, "reason", workerPeerResponse.Reason, "node name", manager.nodeName)
		return false
	}

}

func (manager *Manager) isDiagnosticsPassed() bool {
	manager.log.Info("Starting control-plane node diagnostics")
	if manager.isEndpointAccessLost() {
		return false
	} else if !manager.isKubeletServiceRunning() {
		return false
	}
	manager.log.Info("Control-plane node diagnostics passed successfully")
	return true
}

func wrapWithInitError(err error) error {
	return fmt.Errorf("error initializing controlplane handler [%w]", err)
}

func (manager *Manager) initializeManager() error {

	node := corev1.Node{}
	key := client.ObjectKey{
		Name: manager.nodeName,
	}

	if err := manager.client.Get(context.TODO(), key, &node); err != nil {
		manager.log.Error(err, "could not retrieve node")
		return wrapWithInitError(err)
	}
	manager.setNodeRole(node)
	manager.nodeAddresses = node.Status.Addresses

	manager.wasEndpointAccessibleAtStart = manager.isEndpointAccessible()
	return nil
}

func (manager *Manager) setNodeRole(node corev1.Node) {
	if nodes.IsControlPlane(&node) {
		manager.nodeRole = peers.ControlPlane
	} else {
		manager.nodeRole = peers.Worker
	}
}

func (manager *Manager) isEndpointAccessLost() bool {
	if !manager.wasEndpointAccessibleAtStart {
		return false
	}
	return !manager.isEndpointAccessible()
}

func (manager *Manager) isEndpointAccessible() bool {
	if len(manager.endpointHealthCheckUrl) == 0 {
		return true
	}

	// re-resolve EndpointHealthCheckUrl and, if needed, patch its
	// NetworkPolicy egress exception
	patched := manager.ensureEndpointNetworkPolicyUpToDate(context.TODO())

	if manager.pingEndpoint() {
		return true
	}
	if !patched {
		return false
	}

	// We just patched the NetworkPolicy because EndpointHealthCheckUrl
	// resolved to a new address. Enforcing that patch in the cluster's CNI
	// is asynchronous, so a ping made immediately after patching can still
	// be blocked by the old rule for a short window. Retry a few times
	for i := 0; i < endpointAccessRetriesAfterNetworkPolicyPatch; i++ {
		time.Sleep(endpointAccessRetryBackoff)
		if manager.pingEndpoint() {
			return true
		}
	}
	return false
}

func (manager *Manager) pingEndpoint() bool {
	pinger, err := ping.NewPinger(manager.endpointHealthCheckUrl)
	if err != nil {
		manager.log.Error(err, "could not access endpoint", "endpoint URL", manager.endpointHealthCheckUrl)
		return false
	}
	pinger.Count = 3
	pinger.Timeout = time.Second * 5

	if err := pinger.Run(); err != nil {
		manager.log.Error(err, "could not access endpoint", "endpoint URL", manager.endpointHealthCheckUrl)
		return false
	}

	// pinger.Run() only returns an error on setup/socket failures. If every
	// ping request times out, Run() still returns nil, so we must check that
	// at least one reply was received before declaring the endpoint accessible.
	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		manager.log.Error(fmt.Errorf("no ping replies received"), "could not access endpoint", "endpoint URL", manager.endpointHealthCheckUrl, "packetsSent", stats.PacketsSent)
		return false
	}
	return true
}

// ensureEndpointNetworkPolicyUpToDate re-resolves endpointHealthCheckUrl and,
// if its address(es) changed since the NetworkPolicy was last written,
// patches that NetworkPolicy's egress exception for it.
func (manager *Manager) ensureEndpointNetworkPolicyUpToDate(ctx context.Context) bool {
	if manager.namespace == "" {
		return false
	}

	cidrs, err := utils.ResolveURLToSortedCIDRs(manager.endpointHealthCheckUrl)
	if err != nil {
		manager.log.Error(err, "could not resolve endpoint health check URL to an IP; leaving its NetworkPolicy egress exception as-is", "endpoint URL", manager.endpointHealthCheckUrl)
		return false
	}

	np := &networkingv1.NetworkPolicy{}
	key := client.ObjectKey{Name: endpointNetworkPolicyName, Namespace: manager.namespace}
	if err := manager.client.Get(ctx, key, np); err != nil {
		manager.log.Error(err, "could not get NetworkPolicy to check its endpoint egress exception", "name", endpointNetworkPolicyName, "namespace", manager.namespace)
		return false
	}

	if sameCIDRs(endpointCIDRsFromEgress(np.Spec.Egress), cidrs) {
		return false
	}

	np.Spec.Egress = replaceEndpointCIDRRules(np.Spec.Egress, cidrs)
	if err := manager.client.Update(ctx, np); err != nil {
		manager.log.Error(err, "could not update NetworkPolicy's endpoint egress exception to the newly resolved address(es)", "name", endpointNetworkPolicyName, "namespace", manager.namespace)
		return false
	}
	manager.log.Info("updated NetworkPolicy's endpoint egress exception to the newly resolved address(es)",
		"name", endpointNetworkPolicyName, "namespace", manager.namespace, "cidrs", cidrs)
	return true
}

func isEndpointCIDRRule(rule networkingv1.NetworkPolicyEgressRule) bool {
	return len(rule.Ports) == 0 && len(rule.To) == 1 && rule.To[0].IPBlock != nil
}

func endpointCIDRsFromEgress(egress []networkingv1.NetworkPolicyEgressRule) []string {
	var cidrs []string
	for _, rule := range egress {
		if isEndpointCIDRRule(rule) {
			cidrs = append(cidrs, rule.To[0].IPBlock.CIDR)
		}
	}
	sort.Strings(cidrs)
	return cidrs
}

func replaceEndpointCIDRRules(egress []networkingv1.NetworkPolicyEgressRule, cidrs []string) []networkingv1.NetworkPolicyEgressRule {
	kept := make([]networkingv1.NetworkPolicyEgressRule, 0, len(egress)+len(cidrs))
	for _, rule := range egress {
		if !isEndpointCIDRRule(rule) {
			kept = append(kept, rule)
		}
	}
	for _, cidr := range cidrs {
		kept = append(kept, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
		})
	}
	return kept
}

func sameCIDRs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (manager *Manager) isKubeletServiceRunning() bool {
	for _, addressType := range manager.preferredAddressTypes {
		if addressType == nodeNameAddressType {
			if manager.isKubeletServiceRunningOnAddress(manager.nodeName) {
				return true
			}
		} else {
			nodeAddressType := corev1.NodeAddressType(addressType)
			for _, address := range manager.nodeAddresses {
				if address.Type == nodeAddressType {
					if manager.isKubeletServiceRunningOnAddress(address.Address) {
						return true
					}
				}
			}
		}
	}

	return false
}

func (manager *Manager) isKubeletServiceRunningOnAddress(address string) bool {
	url := fmt.Sprintf("https://%s/pods", net.JoinHostPort(address, manager.kubeletPort))
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         certificates.TLSMinVersion,
		},
	}
	httpClient := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		manager.log.Error(err, "failed to create a kubelet service request", "address", address)
		return false
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		manager.log.Error(err, "kubelet service is down", "address", address)
		return false
	}
	defer resp.Body.Close()
	return true
}
