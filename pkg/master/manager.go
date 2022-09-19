package master

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net"
	"os/exec"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"time"
)

const (
	initErrorText                = "error initializing master handler"
	etcdContainerName            = "etcd"
	kubeletPort                  = "10250"
	etcdPort                     = "2379"
	recoverAttemptsFromIsolation = 10
)

var (
	initError    = errors.New(initErrorText)
	processError = errors.New("an error occurred during master remediation process")
)

//Manager contains logic and info needed to fence and remediate master nodes
type Manager struct {
	nodeName string
	nodeRole peers.Role
	//TODO mshitrit remove if not used
	isHasInternetAccess                  bool
	client                               client.Client
	log                                  logr.Logger
	exec                                 podCommandExecuter
	etcdPod                              corev1.Pod
	nodeInternalIP                       string
	currentAttemptToRecoverFromIsolation uint8
}

//NewManager inits a new Manager return nil if init fails
func NewManager(nodeName string, myClient client.Client) *Manager {
	return &Manager{
		nodeName:            nodeName,
		client:              myClient,
		isHasInternetAccess: false,
		log:                 ctrl.Log.WithName("master").WithName("Manager"),
		exec:                &basePodCommandExecuter{ctrl.Log.WithName("master").WithName("PodCommandExecuter")},
	}
}

func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.initializeManager(); err != nil {
		return err
	}
	//TODO mshitrit remove later, only for debug
	manager.log.Info("[DEBUG] current node role is:", "role", manager.nodeRole)
	return nil
}

func (manager *Manager) IsMaster() bool {
	return manager.nodeRole == peers.Master
}

func (manager *Manager) IsMasterHealthy(workerPeerResponse peers.Response, isOtherMastersCanBeReached bool) bool {
	manager.log.Info("[DEBUG] 3 - IsMasterHealthy starting ... ", "isOtherMastersCanBeReached", isOtherMastersCanBeReached, "workerPeerResponse", workerPeerResponse)
	prevAttemptsNumber := manager.currentAttemptToRecoverFromIsolation
	manager.currentAttemptToRecoverFromIsolation = 0
	switch workerPeerResponse.Reason {
	//reported unhealthy by worker peers
	case peers.UnHealthyBecauseCRFound:
		manager.log.Info("[DEBUG] 3.1 - IsMasterHealthy done", "return value", false)
		return false
	case peers.UnHealthyBecauseNodeIsIsolated:
		manager.log.Info("[DEBUG] 3.2 - IsMasterHealthy done", "return value", isOtherMastersCanBeReached, "attempt #", prevAttemptsNumber+1)
		if !isOtherMastersCanBeReached {
			manager.currentAttemptToRecoverFromIsolation = prevAttemptsNumber
			manager.currentAttemptToRecoverFromIsolation++
			return manager.currentAttemptToRecoverFromIsolation < recoverAttemptsFromIsolation
		}
		return isOtherMastersCanBeReached
	//reported healthy by worker peers
	case peers.HealthyBecauseErrorsThresholdNotReached, peers.HealthyBecauseCRNotFound:
		manager.log.Info("[DEBUG] 3.3 - IsMasterHealthy done", "return value", true)
		return true
	//master node has connection to most workers, we assume it's not isolated (or at least that the master node that does not have worker peers quorum will reboot)
	case peers.HealthyBecauseMostPeersCantAccessAPIServer:
		//TODO mshitrit error is ignored
		isHealthy, _ := manager.isDiagnosticsPassed()
		manager.log.Info("[DEBUG] 3.4 - IsMasterHealthy done", "return value", isHealthy)
		return isHealthy
	case peers.HealthyBecauseNoPeersWereFound:
		if isHealthy, _ := manager.isDiagnosticsPassed(); !isHealthy {
			manager.log.Info("[DEBUG] 3.5 - IsMasterHealthy done", "return value", false)
			return false
		}
		manager.log.Info("[DEBUG] 3.6 - IsMasterHealthy done", "return value", isOtherMastersCanBeReached)
		return isOtherMastersCanBeReached

	default:
		manager.log.Error(processError, "node is considered unhealthy by worker peers for an unknown reason", "reason", workerPeerResponse.Reason, "node name", manager.nodeName)
		manager.log.Info("[DEBUG] 3.7 - IsMasterHealthy done", "return value", false)
		return false
	}

}

func (manager *Manager) isDiagnosticsPassed() (bool, error) {
	manager.log.Info("[DEBUG] 4 - isDiagnosticsPassed starting")

	if isLostInternetConnection := manager.isHasInternetAccess && !isHasInternetAccess(); isLostInternetConnection {
		manager.log.Info("[DEBUG] 4.1 - isDiagnosticsPassed done", "return value", false)
		return false, nil
	} else if !manager.isKubeletServiceRunning() {
		manager.log.Info("[DEBUG] 4.2 - isDiagnosticsPassed done", "return value", false)
		return false, nil
	}
	manager.log.Info("[DEBUG] 4.3 - isDiagnosticsPassed etcd diagnostic is disabled !")
	/*else if !manager.isEtcdRunning() {
		manager.log.Info("[DEBUG] 4.3 - isDiagnosticsPassed done", "return value", false)
		return false, nil
	}*/
	manager.log.Info("[DEBUG] 4.4 - isDiagnosticsPassed done", "return value", true)
	return true, nil
}

func wrapWithInitError(err error) error {
	return fmt.Errorf(initErrorText+" [%w]", err)
}

func (manager *Manager) initializeManager() error {
	nodesList := &corev1.NodeList{}
	if err := manager.client.List(context.TODO(), nodesList, &client.ListOptions{}); err != nil {
		manager.log.Error(err, "could not retrieve nodes")
		return wrapWithInitError(err)
	}
	var node corev1.Node
	for _, n := range nodesList.Items {
		if n.Name == manager.nodeName {
			manager.setNodeRole(n)
			node = n
			break
		}
	}

	if &node == nil {
		manager.log.Error(initError, "could not find node")
		return initError
	}

	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			manager.nodeInternalIP = address.Address
			break
		}
	}

	if len(manager.nodeInternalIP) == 0 {
		manager.log.Error(initError, "could not find node's internal IP", "node name", node.Name)
		return initError
	}

	return nil
}

func (manager *Manager) setNodeRole(node corev1.Node) {
	if _, isWorker := node.Labels[peers.WorkerLabelName]; isWorker {
		manager.nodeRole = peers.Worker
	} else {
		peers.SetControlPlaneLabelType(&node)
		if _, isMaster := node.Labels[peers.GetUsedControlPlaneLabel()]; isMaster {
			manager.nodeRole = peers.Master
		}
	}
}

func (manager *Manager) fetchEtcdPod() corev1.Pod {
	podsList := &corev1.PodList{}
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{"etcd": "true"}}
	selector, _ := metav1.LabelSelectorAsSelector(&labelSelector)
	listOptions := client.ListOptions{
		LabelSelector: selector,
	}
	if err := manager.client.List(context.TODO(), podsList, &listOptions); err != nil {
		manager.log.Info("[DEBUG] etcd pod isn't found, couldn't fetch any pods")
		return manager.etcdPod
	}
	isFound := false
	for _, pod := range podsList.Items {
		if pod.Name == fmt.Sprintf("etcd-%s", manager.nodeName) {
			manager.etcdPod = pod
			manager.log.Info("[DEBUG] etcd pod found")
			isFound = true
			break
		}
	}
	if !isFound {
		manager.log.Info("[DEBUG] etcd pod isn't found", "#pods fetched", len(podsList.Items))
	}

	return manager.etcdPod
}

func isHasInternetAccess() bool {
	fmt.Println("[DEBUG] 5 - isHasInternetAccess starting")
	con, err := net.DialTimeout("tcp", "google.com:80", 5*time.Second)
	defer func(con net.Conn) {
		_ = con.Close()
	}(con)
	fmt.Println("[DEBUG] 5.1 - isHasInternetAccess done", "return value", err == nil)

	return err == nil
}

func (manager *Manager) isKubeletServiceRunning() bool {

	manager.log.Info("[DEBUG] 6 - isKubeletServiceRunning starting")
	url := fmt.Sprintf("https://%s:%s/pods", manager.nodeName, kubeletPort)
	cmd := exec.Command("curl", "-k", "-X", "GET", url)
	if err := cmd.Run(); err != nil {
		manager.log.Error(err, "kubelet service is down", "node name", manager.nodeName)
		manager.log.Info("[DEBUG] 6.1 - isKubeletServiceRunning done", "return value", false)

		return false
	}
	manager.log.Info("[DEBUG] 6.2 - isKubeletServiceRunning done", "return value", true)

	return true
}

func (manager *Manager) isEtcdRunning() bool {
	manager.log.Info("[DEBUG] 7 - isEtcdRunning starting")
	etcdPod := manager.fetchEtcdPod()

	stdout, stderr, err := manager.exec.execCmdOnPod([]string{"etcdctl", "endpoint", "health"}, &etcdPod, etcdContainerName)

	if len(stdout) > 0 {
		isHealthy := strings.Contains(stdout, fmt.Sprintf("%s:%s is healthy", manager.nodeInternalIP, etcdPort))
		manager.log.Info("[DEBUG] isEtcdRunning results", "isHealthy", isHealthy, "stdout", stdout, "stderr", stderr, "err", err)
		manager.log.Info("[DEBUG] 7.1 - isEtcdRunning done", "return value", isHealthy)
		return isHealthy
	} else {
		manager.log.Info("[DEBUG] isEtcdRunning results", "isHealthy", false, "stdout", stdout, "stderr", stderr, "err", err)
		manager.log.Info("[DEBUG] 7.2 - isEtcdRunning done", "return value", false)
		return false
	}
}
