/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	poisonPill "github.com/n1r1/poison-pill/api"
	"github.com/n1r1/poison-pill/utils"
	wdt "github.com/n1r1/poison-pill/watchdog"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	externalRemediationAnnotation = "host.metal3.io/external-remediation"
	nodeBackupAnnotation          = "poison-pill.openshift.io/node-backup"
	machineAnnotationKey          = "machine.openshift.io/machine"
	nodeNameEnvVar                = "MY_NODE_NAME"
	maxFailuresThreshold          = 3 //after which we start asking peers for health status
	peersProtocol                 = "http"
	peersPort                     = 30001
	apiServerTimeout              = 5 * time.Second
	peerTimeout                   = 10 * time.Second
	//note that this time must include the time for a unhealthy node without api-server access to reach the conclusion that it's unhealthy
	// this should be at least peersCount * 1s (reconcile interval) * request context timeout
	safeTimeToAssumeNodeRebooted = 90 * time.Second
	waitingForNode               = "waiting-for-node"
)

var (
	reconcileInterval = 15 * time.Second
	nodes             *v1.NodeList
	//nodes to ask for health results
	nodesToAsk            *v1.NodeList
	errCount              int
	apiErrorResponseCount int
	myMachineName         string
	shouldReboot          bool
	lastReconcileTime     time.Time
	watchdog              wdt.Watchdog
	myNodeName            = os.Getenv(nodeNameEnvVar)
	httpClient            = &http.Client{
		Timeout: peerTimeout,
	}

	NodeUnschedulableTaint = &v1.Taint{
		Key:    "node.kubernetes.io/unschedulable",
		Effect: v1.TaintEffectNoSchedule,
	}
)

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	ApiReader client.Reader //todo make sure everywhere we use apiReader unless special case
}

// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines/status,verbs=get;update;patch
func (r *MachineReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	if shouldReboot {
		return r.reboot()
	}

	// set myMachineName global variable to the machine this pod runs on
	if myMachineName == "" {
		return r.getMachineName()
	}

	//try to create watchdog if not exists
	if watchdog == nil {
		if wdt.IsWatchdogAvailable() {
			if err := r.initWatchdog(); err != nil {
				r.Log.Error(err, "failed to start watchdog device")
				return ctrl.Result{RequeueAfter: reconcileInterval}, err
			}
		}
	} else {
		if err := watchdog.Feed(); err != nil {
			r.Log.Error(err, "failed to feed watchdog")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, err
		}
	}

	machine := &machinev1beta1.Machine{}

	//define context with timeout, otherwise this blocks forever
	ctx, cancelFunc := context.WithTimeout(context.TODO(), apiServerTimeout)
	defer cancelFunc()

	//todo we need to make sure we reconcile this machine, and not only others. if we requeue after constant interval it doesn't mean we reconcile the correct machine

	//we use ApiReader as it doesn't use the cache. Otherwise, the cache returns the object
	//even though the api server is not available
	err := r.ApiReader.Get(ctx, request.NamespacedName, machine)

	if err != nil {
		return r.handleApiError(err)
	}

	//if there was no error we reset the global error count
	errCount = 0
	apiErrorResponseCount = 0

	_ = r.updateNodesList()

	if machine.Annotations != nil {
		if lastUnhealthyTimeStr, exists := machine.Annotations[externalRemediationAnnotation]; exists {
			return r.handleUnhealthyMachine(lastUnhealthyTimeStr, machine)
		}
	}

	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
}

func (r *MachineReconciler) initWatchdog() error {
	r.Log.Info("starting watchdog")
	w, err := wdt.StartWatchdog()
	watchdog = wdt.Watchdog(w)
	if err != nil {
		return err
	}

	if watchdogTimeout, err := watchdog.GetTimeout(); err != nil {
		r.Log.Error(err, "failed to retrieve watchdog timeout")
	} else {
		r.Log.Info("retrieved watchdog timeout", "timeout", watchdogTimeout)
		//todo reconcileInterval = watchdogTimeout / 2
	}
	return nil
}

//retrieves the node that runs this pod
func (r *MachineReconciler) getMyNode() (*v1.Node, error) {
	nodeNamespacedName := types.NamespacedName{
		Namespace: "",
		Name:      myNodeName,
	}

	node := &v1.Node{}
	err := r.Get(context.TODO(), nodeNamespacedName, node)

	return node, err
}

// getNodeByMachine returns the node object referenced by machine
func (r *MachineReconciler) getNodeByMachine(machine *machinev1beta1.Machine) (*v1.Node, error) {
	if machine.Status.NodeRef == nil {
		return nil, apiErrors.NewNotFound(v1.Resource("ObjectReference"), machine.Name)
	}

	node := &v1.Node{}
	key := client.ObjectKey{
		Name:      machine.Status.NodeRef.Name,
		Namespace: machine.Status.NodeRef.Namespace,
	}

	if err := r.ApiReader.Get(context.TODO(), key, node); err != nil {
		return nil, err
	}

	return node, nil
}

// updates machineName global variable to the machine associated with the myNodeName
func (r *MachineReconciler) getMachineName() (ctrl.Result, error) {
	r.Log.Info("Determining machine name ...")

	node, err := r.getMyNode()
	if err != nil {
		r.Log.Error(err, "Failed to retrieve node object", "node name", myNodeName)
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	if node.Annotations == nil {
		err := errors.New("no annotations on node. Can't determine machine name")
		r.Log.Error(err, "")
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	machine, machineExists := node.Annotations[machineAnnotationKey]

	if !machineExists {
		err := errors.New("machine annotation is not present on the node. can't determine machine name")
		r.Log.Error(err, "")
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	myMachineName = strings.Split(machine, string(types.Separator))[1]
	r.Log.Info("Detected machine name", "my machine name", myMachineName)
	return ctrl.Result{Requeue: true}, nil
}

func (r *MachineReconciler) restoreNode(machine *machinev1beta1.Machine, nodeBackup string) (ctrl.Result, error) {
	r.Log.Info("found backup node on machine. restoring node", "machine name", machine.Name)

	nodeToRestore := &v1.Node{}

	if err := json.Unmarshal([]byte(nodeBackup), nodeToRestore); err != nil {
		r.Log.Error(err, "failed to unmarshal node")
		return ctrl.Result{}, err
	}

	nodeToRestore.ResourceVersion = "" //create won't work with a non-empty value here
	taints, _ := utils.DeleteTaint(nodeToRestore.Spec.Taints, NodeUnschedulableTaint)
	nodeToRestore.Spec.Taints = taints
	nodeToRestore.Spec.Unschedulable = false

	if err := r.Client.Create(context.TODO(), nodeToRestore); err != nil {
		if apiErrors.IsAlreadyExists(err) {
			delete(machine.Annotations, nodeBackupAnnotation)
			if err := r.Client.Update(context.TODO(), machine); err != nil {
				r.Log.Error(err, "failed to remove node backup annotation")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		r.Log.Error(err, "failed to create node")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
}

//handleUnhealthyMachine is taking the steps to recover an unhealthy machine which has api-server access
func (r *MachineReconciler) handleUnhealthyMachine(lastUnhealthyTimeStr string, machine *machinev1beta1.Machine) (ctrl.Result, error) {
	r.Log.Info("found external remediation annotation", "machine name", machine.Name)

	// we use lastUnhealthyTimeStr as the state of the remediation process, MHC set it first to
	// an empty value
	switch lastUnhealthyTimeStr {
	case "": // step #1
		return r.handleEmptyAnnotationValue(machine)
	case waitingForNode: // step #3
		return r.handleWaitingForNode(machine)
	default: // step #2
		lastUnhealthyTime, err := time.Parse(time.RFC3339, lastUnhealthyTimeStr)
		if err != nil {
			r.Log.Error(err, "failed to parse time from unhealthy annotation", "machine name", machine.Name)
			return ctrl.Result{}, err
		}
		return r.handleTimeValueInAnnotation(lastUnhealthyTime, machine)
	}
}

//handleEmptyAnnotationValue cordons the node associated with the given machine, sets the current time
//in externalRemediationAnnotation and add the node CR to nodeBackupAnnotation
func (r *MachineReconciler) handleEmptyAnnotationValue(machine *machinev1beta1.Machine) (ctrl.Result, error) {
	node, err := r.getNodeByMachine(machine)

	if err != nil {
		r.Log.Error(err, "failed to get node CR", "machine name", machine.Name)
		return ctrl.Result{}, err
	}

	if !node.Spec.Unschedulable {
		return r.markNodeAsUnschedulable(node)
	}

	if !utils.TaintExists(node.Spec.Taints, NodeUnschedulableTaint) {
		r.Log.Info("waiting for unschedulable taint to appear", "node name", node.Name)
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	marshaledNode, err := json.Marshal(node)
	if err != nil {
		r.Log.Error(err, "failed to marshal node", "machine name", machine.Name)
		return ctrl.Result{}, err
	}

	//we assume the unhealthy machine will be rebooted after this time + safeTimeToAssumeNodeRebooted
	machine.Annotations[externalRemediationAnnotation] = time.Now().Format(time.RFC3339)
	//backup the node CR, as it's going to be deleted and we want to restore it
	machine.Annotations[nodeBackupAnnotation] = string(marshaledNode)

	r.Log.Info("updating machine with node CR backup and updating unhealthy time", "machine name", machine.Name)

	if err := r.Client.Update(context.TODO(), machine); err != nil {
		if apiErrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		r.Log.Error(err, "failed to update machine with node backup and unhealthy time", "machine name", machine.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

//handleTimeValueInAnnotation reboots itself if still within the reboot time, deletes the node, and sets the state to waitingForNode
func (r *MachineReconciler) handleTimeValueInAnnotation(lastUnhealthyTime time.Time, machine *machinev1beta1.Machine) (ctrl.Result, error) {
	maxNodeRebootTime := lastUnhealthyTime.Add(safeTimeToAssumeNodeRebooted)
	if maxNodeRebootTime.After(time.Now()) {
		if machine.Name == myMachineName {
			r.stopWatchdogFeeding()
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: maxNodeRebootTime.Sub(time.Now())}, nil
	}

	r.Log.Info("annotation time is old. The unhealthy machine assumed to been rebooted", "machine name", machine.Name)

	node, err := r.getNodeByMachine(machine)

	if apiErrors.IsNotFound(err) {
		machine.Annotations[externalRemediationAnnotation] = waitingForNode

		if err := r.Client.Update(context.TODO(), machine); err != nil {
			r.Log.Error(err, "failed to update external remediation annotation", "machine name", machine.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if err != nil {
		r.Log.Error(err, "failed to get node CR for deletion", "machine name", machine.Name)
		return ctrl.Result{}, err
	}

	if !node.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	r.Log.Info("deleting unhealthy node", "node name", node.Name)
	if err := r.Client.Delete(context.TODO(), node); err != nil {
		r.Log.Error(err, "failed to delete the unhealthy node")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

//handleWaitingForNode restores node from machine annotation and deletes the nodeBackupAnnotation
//and the externalRemediationAnnotation
func (r *MachineReconciler) handleWaitingForNode(machine *machinev1beta1.Machine) (ctrl.Result, error) {
	if _, err := r.getNodeByMachine(machine); err != nil {
		if apiErrors.IsNotFound(err) {
			if nodeBackup, nodeBackupExists := machine.Annotations[nodeBackupAnnotation]; nodeBackupExists {
				return r.restoreNode(machine, nodeBackup)
			}
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	r.Log.Info("node has been restored. removing unhealthy annotation and node backup annotation", "machine name", machine.Name)

	delete(machine.Annotations, nodeBackupAnnotation)
	delete(machine.Annotations, externalRemediationAnnotation)

	if err := r.Client.Update(context.TODO(), machine); err != nil {
		r.Log.Error(err, "failed to remove external remediation annotation and node backup annotation", "machine name", machine.Name)
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *MachineReconciler) markNodeAsUnschedulable(node *v1.Node) (ctrl.Result, error) {
	node.Spec.Unschedulable = true
	r.Log.Info("Marking node as unschedulable", "node name", node.Name)
	if err := r.Client.Update(context.TODO(), node); err != nil {
		r.Log.Error(err, "failed to mark node as unschedulable")
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
}

//handleApiError handles the case where the api server is not responding or responding with an error
func (r *MachineReconciler) handleApiError(err error) (ctrl.Result, error) {
	//we don't want to increase the err count too quick
	minTimeToReconcile := lastReconcileTime.Add(reconcileInterval)
	if !time.Now().After(minTimeToReconcile) {
		return ctrl.Result{RequeueAfter: minTimeToReconcile.Sub(time.Now())}, nil
	}
	lastReconcileTime = time.Now()

	errCount++
	r.Log.Error(err, "failed to retrieve machine from api-server", "error count is now", errCount)

	if errCount <= maxFailuresThreshold {
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	r.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
	if nodes == nil || len(nodes.Items) == 0 {
		if err := r.updateNodesList(); err != nil {
			r.Log.Error(err, "peers list is empty and couldn't be retrieved from server")
			return ctrl.Result{RequeueAfter: reconcileInterval}, err
		}
		//todo maybe we need to check if this happens too much and reboot
	}

	if nodesToAsk == nil || len(nodesToAsk.Items) == 0 {
		//deep copy nodes to ask, as we're going to remove nodes we already asked from the list
		nodesToAsk = nodes.DeepCopy()
	}

	nodesBatchCount := len(nodes.Items) / 10
	if nodesBatchCount == 0 {
		nodesBatchCount = 1
	}

	chosenNodesAddresses := popNodes(nodesToAsk, nodesBatchCount)
	responsesChan := make(chan poisonPill.HealthCheckResponse, nodesBatchCount)

	for _, address := range chosenNodesAddresses {
		go r.getHealthStatusFromPeer(address, responsesChan)
	}

	healthyResponses, unhealthyResponses, apiErrorsResponses, _ := r.sumPeersResponses(nodesBatchCount, responsesChan)

	if healthyResponses > 0 {
		r.Log.Info("Peer told me I'm healthy.")
		apiErrorResponseCount = 0
		errCount = 0
		nodesToAsk = nil
		return ctrl.Result{RequeueAfter: reconcileInterval}, nil
	}

	if unhealthyResponses > 0 {
		r.Log.Info("Peer told me I'm unhealthy. Stopping watchdog feeding")
		r.stopWatchdogFeeding()
		return ctrl.Result{RequeueAfter: 1 * time.Second}, err
	}

	if apiErrorsResponses > 0 {
		apiErrorResponseCount += apiErrorsResponses
		if apiErrorResponseCount > len(nodes.Items)/2 { //already reached more than 50% of the nodes and all of them returned api error
			//assuming this is a control plane failure as others can't access api-server as well
			r.Log.Info("More than 50% of the nodes couldn't access the api-server, assuming this is a control plane failure")
			errCount = 0
			nodesToAsk = nil
			apiErrorResponseCount = 0
			return ctrl.Result{RequeueAfter: reconcileInterval}, nil
		}
	}

	if len(nodesToAsk.Items) == 0 {
		//we already asked all peers
		r.Log.Error(err, "failed to get health status from the last peer in the list. Assuming unhealthy")
		r.stopWatchdogFeeding()
	}

	return ctrl.Result{RequeueAfter: 1 * time.Second}, err
}

func (r *MachineReconciler) sumPeersResponses(nodesBatchCount int, responsesChan chan poisonPill.HealthCheckResponse) (int, int, int, int) {
	healthyResponses := 0
	unhealthyResponses := 0
	apiErrorsResponses := 0
	noResponse := 0

	for i := 0; i < nodesBatchCount; i++ {
		response := <-responsesChan
		r.Log.Info("got response from peer", "response", response)

		switch response {
		case poisonPill.Unhealthy:
			healthyResponses++
			break
		case poisonPill.Healthy:
			unhealthyResponses++
			break
		case poisonPill.ApiError:
			apiErrorsResponses++
			break
		case -1:
			noResponse++

		default:
			r.Log.Error(errors.New("got unexpected value from peer while trying to retrieve health status"),
				"Received value", response)
		}
	}

	return healthyResponses, unhealthyResponses, apiErrorsResponses, noResponse
}

// reboot stops watchdog feeding ig wachdog exists, otherwise issuing a sowtware reboot
func (r *MachineReconciler) reboot() (ctrl.Result, error) {
	if watchdog == nil {
		r.Log.Info("no watchdog is present on this host, trying software reboot")
		//we couldn't init a watchdog so far but requested to be rebooted. we issue a software reboot
		if err := softwareReboot(); err != nil {
			r.Log.Error(err, "failed to run reboot command")
			return ctrl.Result{RequeueAfter: reconcileInterval}, err
		}
		return ctrl.Result{RequeueAfter: reconcileInterval}, nil
	}
	//we stop feeding the watchdog and waiting for a reboot
	r.Log.Info("watchdog feeding has stopped, waiting for reboot to commence")
	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
}

//updates nodes global variable to include list of the nodes objects that exists in api-server
func (r *MachineReconciler) updateNodesList() error {
	nodeList := &v1.NodeList{}

	if err := r.List(context.TODO(), nodeList); err != nil {
		r.Log.Error(err, "failed to get nodes list")
		return err
	}

	//store the node list, so that it will be available if api-server is not accessible at some point
	nodes = nodeList.DeepCopy()
	for i, node := range nodes.Items {
		//remove the node this pod runs on from the list
		if node.Name == myNodeName {
			nodes.Items = append(nodes.Items[:i], nodes.Items[i+1:]...)
		}
	}
	return nil
}

//getHealthStatusFromPeer issues a GET request to the specified IP and returns the result from the peer into the given channel
func (r *MachineReconciler) getHealthStatusFromPeer(endpointIp string, results chan<- poisonPill.HealthCheckResponse) {
	url := fmt.Sprintf("%s://%s:%d/health/%s", peersProtocol, endpointIp, peersPort, myMachineName)

	resp, err := httpClient.Get(url)

	if err != nil {
		r.Log.Error(err, "failed to get health status from peer", "url", url)
		results <- -1
		return
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		r.Log.Error(err, "failed to read health response from peer")
		results <- -1
		return
	}

	healthStatusResult, err := strconv.Atoi(string(body))

	if err != nil {
		r.Log.Error(err, "failed to convert health check response from string to int")
	}

	results <- poisonPill.HealthCheckResponse(healthStatusResult)
	return
}

//this will cause reboot
func (r *MachineReconciler) stopWatchdogFeeding() {
	r.Log.Info("No more food for watchdog... system will be rebooted...", "node name", myNodeName)
	shouldReboot = true
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&machinev1beta1.Machine{}).
		Complete(r)
}

//popNode recieves non-empty NodeList and return the IP address of one of the nodes from that list
func popNode(nodes *v1.NodeList) (address string) {
	if len(nodes.Items) == 0 {
		return ""
	}

	//todo this should be random, check if address exists, etc.
	nodeIndex := 0 //rand.Intn(len(nodes.Items))
	chosenNode := nodes.Items[nodeIndex]
	address = chosenNode.Status.Addresses[0].Address //todo node might have multiple addresses
	nodes.Items = nodes.Items[1:]                    //remove this node from the list

	return
}

func popNodes(nodes *v1.NodeList, count int) []string {
	addresses := make([]string, 10)
	if len(nodes.Items) == 0 {
		return addresses
	}

	//todo this should be random, check if address exists, etc.
	nodeIndex := 0 //rand.Intn(len(nodes.Items))
	chosenNode := nodes.Items[nodeIndex]

	nodesCount := count
	if nodesCount > len(nodes.Items) {
		nodesCount = len(nodes.Items)
	}

	for i := 0; i < nodesCount; i++ {
		addresses[i] = chosenNode.Status.Addresses[i].Address //todo node might have multiple addresses
	}

	nodes.Items = nodes.Items[nodesCount:] //remove popped nodes from the list

	return addresses
}

//softwareReboot performs software reboot by running systemctl reboot
func softwareReboot() error {
	// hostPID: true and privileged:true required to run this
	rebootCmd := exec.Command("/usr/bin/nsenter", "-m/proc/1/ns/mnt", "/bin/systemctl", "reboot")
	return rebootCmd.Run()
}
