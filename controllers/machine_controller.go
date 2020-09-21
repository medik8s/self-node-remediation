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
	maxFailuresThreshold          = 3
	peersProtocol                 = "http"
	peersPort                     = 30001
	reconcileInterval             = 15 * time.Second
	apiServerTimeout              = 5 * time.Second
	peerTimeout                   = 10 * time.Second
	safeTimeToAssumeNodeRebooted  = 90 * time.Second
	waitingForNode                = "waiting-for-node"
)

var (
	nodes             *v1.NodeList
	nodesToAsk        *v1.NodeList
	errCount          int
	myMachineName     string
	shouldReboot      bool
	lastReconcileTime time.Time
	watchdog          *wdt.Watchdog
	myNodeName        = os.Getenv(nodeNameEnvVar)
	httpClient        = &http.Client{
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

// updates machineName to the machine associated with the myNodeName
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
	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
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

func (r *MachineReconciler) handleUnhealthyMachine(lastUnhealthyTimeStr string, machine *machinev1beta1.Machine) (ctrl.Result, error) {
	r.Log.Info("found external remediation annotation", "machine name", machine.Name)

	if lastUnhealthyTimeStr == "" {
		node, err := r.getNodeByMachine(machine)

		if err != nil {
			r.Log.Error(err, "failed to get node CR", "machine name", machine.Name)
			return ctrl.Result{}, err
		}

		if !node.Spec.Unschedulable {
			node.Spec.Unschedulable = true
			r.Log.Info("Marking node as unschedulable", "node name", node.Name)
			if err := r.Client.Update(context.TODO(), node); err != nil {
				r.Log.Error(err, "failed to mark node as unschedulable")
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
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

		machine.Annotations[externalRemediationAnnotation] = time.Now().Format(time.RFC3339)
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

	if lastUnhealthyTimeStr == waitingForNode {
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
			r.Log.Error(err, "failed to remove external remediation annotation", "machine name", machine.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil

	}

	lastUnhealthyTime, err := time.Parse(time.RFC3339, lastUnhealthyTimeStr)
	if err != nil {
		r.Log.Error(err, "failed to parse time from unhealthy annotation", "machine name", machine.Name)
		return ctrl.Result{}, err
	}

	if lastUnhealthyTime.Add(safeTimeToAssumeNodeRebooted).After(time.Now()) {
		if machine.Name == myMachineName {
			r.stopWatchdogFeeding()
			return ctrl.Result{Requeue: true}, nil
		}

		return ctrl.Result{RequeueAfter: reconcileInterval}, nil
	}

	r.Log.Info("annotation time is old. The unhealthy machine assumed to been rebooted", "machine name", machine.Name)

	node, err := r.getNodeByMachine(machine)

	if apiErrors.IsNotFound(err) {
		machine.Annotations[externalRemediationAnnotation] = waitingForNode

		if err := r.Client.Update(context.TODO(), machine); err != nil {
			r.Log.Error(err, "failed to remove external remediation annotation", "machine name", machine.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if err != nil {
		r.Log.Error(err, "failed to get node CR for backup before deletion", "machine name", machine.Name)
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

//handleApiError handles the case where the api server is not responding or responding with an error
func (r *MachineReconciler) handleApiError(err error) (ctrl.Result, error) {
	errCount++
	r.Log.Error(err, "failed to retrieve machine from api-server", "error count is now", errCount)

	if errCount > maxFailuresThreshold {
		r.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
		if nodes == nil || len(nodes.Items) == 0 {
			if err := r.updateNodesList(); err != nil {
				r.Log.Error(err, "peers list is empty and couldn't be retrieved from server")
				return ctrl.Result{RequeueAfter: reconcileInterval}, err
			}
		}

		if nodesToAsk == nil || len(nodesToAsk.Items) == 0 {
			nodesToAsk = nodes.DeepCopy()
		}

		result, err := r.getHealthStatusFromPeer(popNode(nodesToAsk))
		if err != nil {
			if len(nodes.Items) == 1 {
				//we got an error from the last node in the list
				r.Log.Error(err, "failed to get health status from the last peer in the list. Assuming unhealthy")
				r.stopWatchdogFeeding() //todo we need to reboot only after someone else marked the node as unschedulable, need to check this is happeps before
			}
			return ctrl.Result{RequeueAfter: reconcileInterval}, err
		}

		r.Log.Info("got response from peer", "response", result)
		r.doSomethingWithHealthResult(result)
	}

	return ctrl.Result{RequeueAfter: reconcileInterval}, err
}

// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines/status,verbs=get;update;patch
func (r *MachineReconciler) Reconcile(request ctrl.Request) (ctrl.Result, error) {
	if shouldReboot {
		if watchdog == nil {
			r.Log.Info("No watchdog is present on this host, trying software reboot")
			//we couldn't init a watchdog so far but requested to be rebooted. we issue a software reboot
			if err := softwareReboot(); err != nil {
				r.Log.Error(err, "failed to run reboot command")
				return ctrl.Result{RequeueAfter: reconcileInterval}, err
			}
			return ctrl.Result{RequeueAfter: reconcileInterval}, nil
		}
		//we stop feeding the watchdog and waiting for a reboot
		r.Log.Info("Watchdog feeding has stopped, waiting for reboot to commence")
		return ctrl.Result{}, nil
	}

	// check what's the machine name I'm running on, only if it's not already known
	if myMachineName == "" {
		return r.getMachineName()
	}

	//try to create watchdog if not exists
	if watchdog == nil {
		if wdt.IsWatchdogAvailable() {
			var err error
			watchdog, err = wdt.StartWatchdog()
			if err != nil {
				return ctrl.Result{RequeueAfter: reconcileInterval}, err
			}
		}
	} else {
		err := watchdog.Feed()
		if err != nil {
			//todo log
			return ctrl.Result{RequeueAfter: 1 * time.Second}, err
		}
	}

	machine := &machinev1beta1.Machine{}

	//define context with timeout, otherwise this blocks forever
	ctx, cancelFunc := context.WithTimeout(context.TODO(), apiServerTimeout)
	defer cancelFunc()

	//todo we need to make sure we reconile this machine, and not only others. if we requeue after constant interval it doesn't mean we reconcile the correct machine

	//we use ApiReader as it doesn't use the cache. Otherwise, the cache returns the object
	//event though the api server is not available
	err := r.ApiReader.Get(ctx, request.NamespacedName, machine)

	if err != nil {
		minTimeToReconcile := lastReconcileTime.Add(reconcileInterval)
		if !time.Now().After(minTimeToReconcile) {
			return ctrl.Result{RequeueAfter: minTimeToReconcile.Sub(time.Now())}, nil
		}

		lastReconcileTime = time.Now()

		return r.handleApiError(err)
	}

	//if there was no error we reset the global error count
	errCount = 0

	_ = r.updateNodesList()

	if lastUnhealthyTimeStr, exists := machine.Annotations[externalRemediationAnnotation]; exists {
		return r.handleUnhealthyMachine(lastUnhealthyTimeStr, machine)
	}

	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
}

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

func (r *MachineReconciler) getHealthStatusFromPeer(endpointIp string) (poisonPill.HealthCheckResponse, error) {
	url := fmt.Sprintf("%s://%s:%d/health/%s", peersProtocol, endpointIp, peersPort, myMachineName)

	resp, err := httpClient.Get(url)

	if err != nil {
		r.Log.Error(err, "Failed to get health status from peer", "url", url)
		return -1, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		//todo
		return -1, err
	}

	healthStatusResult, _ := strconv.Atoi(string(body))

	return poisonPill.HealthCheckResponse(healthStatusResult), nil
}

func (r *MachineReconciler) doSomethingWithHealthResult(healthStatus poisonPill.HealthCheckResponse) {
	switch healthStatus {
	case poisonPill.Unhealthy:
		r.stopWatchdogFeeding()
		break
	case poisonPill.Healthy:
		r.Log.Info("Peer told me I'm healthy. Resetting error count")
		errCount = 0
		nodesToAsk = nil
		break
	case poisonPill.ApiError:
		break
	default:
		r.Log.Error(errors.New("got unexpected value from peer while trying to retrieve health status"), "Received value", healthStatus)
	}
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

func popNode(nodes *v1.NodeList) (address string) {
	if len(nodes.Items) == 0 {
		return "" //todo error
	}

	//todo this should be random, check if address exists, etc.
	nodeIndex := 0 //rand.Intn(len(nodes.Items))
	chosenNode := nodes.Items[nodeIndex]
	address = chosenNode.Status.Addresses[0].Address //todo node might have multiple addresses
	nodes.Items = nodes.Items[1:]                    //remove this node from the list

	return
}

func getRandomNode(nodes *v1.NodeList) *v1.Node {
	if len(nodes.Items) == 0 {
		return nil //todo error
	}

	//todo this should be random, check if address exists, etc.
	nodeIndex := 0 //rand.Intn(len(nodes.Items))
	chosenNode := nodes.Items[nodeIndex]

	return &chosenNode
	//address = chosenNode.Status.Addresses[0].Address //todo node might have multiple addresses
	//nodes.Items = nodes.Items[1:]                    //remove this node from the list
	//
	//return
}

func softwareReboot() error {
	// hostPID: true and privileged:true reqiuired to run this
	rebootCmd := exec.Command("/usr/bin/nsenter", "-m/proc/1/ns/mnt", "/bin/systemctl", "reboot")
	return rebootCmd.Run()
}
