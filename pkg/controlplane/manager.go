package controlplane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-ping/ping"
	"github.com/medik8s/common/pkg/nodes"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/peers"
)

const (
	kubeletPort = "10250"
)

type EvaluationOutcome int

const (
	EvaluationHealthy EvaluationOutcome = iota
	EvaluationRemediate
	EvaluationGlobalOutage
	EvaluationIsolation
	EvaluationAwaitQuorum
)

// Manager contains logic and info needed to fence and remediate controlplane nodes
type Manager struct {
	nodeName                     string
	nodeRole                     peers.Role
	endpointHealthCheckUrl       string
	wasEndpointAccessibleAtStart bool
	client                       client.Client
	log                          logr.Logger
}

// NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) *Manager {
	return &Manager{
		nodeName:                     nodeName,
		endpointHealthCheckUrl:       os.Getenv("END_POINT_HEALTH_CHECK_URL"),
		client:                       myClient,
		wasEndpointAccessibleAtStart: false,
		log:                          ctrl.Log.WithName("controlPlane").WithName("Manager"),
	}
}

func (manager *Manager) Start(_ context.Context) error {
	if err := manager.initializeManager(); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) IsControlPlane() bool {
	manager.log.Info("Checking to see if node is a control plane node",
		"nodeName", manager.nodeName,
		"controlPlaneNodeExpectedValue", peers.ControlPlane,
		"nodeRole", manager.nodeRole)
	return manager.nodeRole == peers.ControlPlane
}

func (manager *Manager) IsControlPlaneHealthy(outcome EvaluationOutcome) bool {
	switch outcome {
	case EvaluationRemediate:
		manager.log.Info("control-plane evaluation requested remediation")
		return false
	case EvaluationIsolation:
		manager.log.Info("control-plane evaluation detected isolation")
		return false
	case EvaluationGlobalOutage:
		didDiagnosticsPass := manager.isDiagnosticsPassed()
		manager.log.Info("peers report API outage, relying on diagnostics",
			"diagnosticsPassed", didDiagnosticsPass)
		return didDiagnosticsPass
	case EvaluationAwaitQuorum:
		manager.log.Info("peer quorum pending, deferring remediation")
		return true
	case EvaluationHealthy:
		return true
	default:
		errorText := "unknown evaluation outcome"
		manager.log.Error(errors.New(errorText), errorText, "outcome", outcome, "node name", manager.nodeName)
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

	manager.wasEndpointAccessibleAtStart = manager.isEndpointAccessible()
	return nil
}

func (manager *Manager) setNodeRole(node corev1.Node) {
	manager.log.Info("setNodeRole called",
		"labels", node.Labels)

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
	return true
}

func (manager *Manager) isKubeletServiceRunning() bool {
	url := fmt.Sprintf("https://%s:%s/pods", manager.nodeName, kubeletPort)
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         certificates.TLSMinVersion,
		},
	}
	httpClient := &http.Client{Transport: tr}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		manager.log.Error(err, "failed to create a kubelet service request", "node name", manager.nodeName)
		return false
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		manager.log.Error(err, "kubelet service is down", "node name", manager.nodeName)
		return false
	}
	defer resp.Body.Close()
	return true
}
