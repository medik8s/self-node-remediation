/*
Copyright 2021.

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
	"errors"
	"fmt"
	poisonPill "github.com/medik8s/poison-pill/api"
	"github.com/medik8s/poison-pill/utils"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	wdt "github.com/medik8s/poison-pill/watchdog"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/poison-pill/api/v1alpha1"
)

const (
	nodeNameEnvVar       = "MY_NODE_NAME"
	maxFailuresThreshold = 3 //after which we start asking peers for health status
	peersProtocol        = "http"
	peersPort            = 30001
	apiServerTimeout     = 5 * time.Second
	peerTimeout          = 10 * time.Second
	//note that this time must include the time for a unhealthy node without api-server access to reach the conclusion that it's unhealthy
	// this should be at least worst-case time to reach a conclusion from the other peers * request context timeout + watchdog interval + maxFailuresThreshold * reconcileInterval + padding
	safeTimeToAssumeNodeRebooted = 90 * time.Second
)

var (
	reconcileInterval = 15 * time.Second
	nodes             *v1.NodeList
	//nodes to ask for health results
	nodesToAsk            *v1.NodeList
	errCount              int
	apiErrorResponseCount int
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

// PoisonPillRemediationReconciler reconciles a PoisonPillRemediation object
type PoisonPillRemediationReconciler struct {
	client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	ApiReader client.Reader
}

//+kubebuilder:rbac:groups=poison-pill.medik8s.io,resources=poisonpillremediations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=poison-pill.medik8s.io,resources=poisonpillremediations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=poison-pill.medik8s.io,resources=poisonpillremediations/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;create;update;patch;delete

func (r *PoisonPillRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("poisonpillremediation", req.NamespacedName)

	if shouldReboot {
		return r.reboot()
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

	//define context with timeout, otherwise this blocks forever
	ctx, cancelFunc := context.WithTimeout(context.TODO(), apiServerTimeout)
	defer cancelFunc()

	ppr := &v1alpha1.PoisonPillRemediation{}
	//we use ApiReader as it doesn't use the cache. Otherwise, the cache returns the object
	//even though the api server is not available
	err := r.ApiReader.Get(ctx, req.NamespacedName, ppr)

	if err != nil {
		if apiErrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: reconcileInterval}, nil
		}
		return r.handleApiError(err)
	}

	//if there was no error we reset the global error count
	errCount = 0
	apiErrorResponseCount = 0

	_ = r.updateNodesList()

	node, err := r.getNodeFromPpr(ppr)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			//as part of the remediation flow, we delete the node, and then we need to restore it
			return r.handleDeletedNode(ppr)
		}
		r.Log.Error(err, "failed to retrieve node", "node name", ppr.Name)
		return ctrl.Result{}, err
	}

	if node.CreationTimestamp.After(ppr.CreationTimestamp.Time) {
		//this node was created after the node was reported as unhealthy
		//we assume this is the new node after remediation and take no-op expecting the ppr to be deleted
		r.Log.Info("node has been restored", "node name", node.Name)
		//todo this means we only allow one remediation attempt per ppr. we could add some config to
		//ppr which states max remediation attempts, and the timeout to consider a remediation failed.
		return ctrl.Result{RequeueAfter: reconcileInterval}, nil
	}

	if !node.Spec.Unschedulable {
		//the unhealthy node might reboot itself and take new workloads
		//since we're going to delete the node eventually, we must make sure the node is deleted
		//when there's no running workload there. Hence we mark it as unschedulable.
		return r.markNodeAsUnschedulable(node)
	}

	if !utils.TaintExists(node.Spec.Taints, NodeUnschedulableTaint) {
		r.Log.Info("waiting for unschedulable taint to appear", "node name", node.Name)
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	if ppr.Status.NodeBackup == nil || ppr.Status.TimeAssumedRebooted.IsZero() {
		return r.updatePprStatus(node, ppr)
	}

	maxNodeRebootTime := ppr.Status.TimeAssumedRebooted

	if maxNodeRebootTime.After(time.Now()) {
		if myNodeName == ppr.Name {
			r.stopWatchdogFeeding()
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: maxNodeRebootTime.Sub(time.Now()) + time.Second}, nil
	}

	r.Log.Info("TimeAssumedRebooted is old. The unhealthy node assumed to been rebooted", "node name", node.Name)

	if !node.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	r.Log.Info("deleting unhealthy node", "node name", node.Name)
	if err := r.Client.Delete(context.TODO(), node); err != nil {
		if !apiErrors.IsNotFound(err) {
			r.Log.Error(err, "failed to delete the unhealthy node")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *PoisonPillRemediationReconciler) updatePprStatus(node *v1.Node, ppr *v1alpha1.PoisonPillRemediation) (ctrl.Result, error) {
	r.Log.Info("updating ppr with node backup and updating time to assume node has been rebooted", "node name", node.Name)
	//we assume the unhealthy node will be rebooted by maxTimeNodeHasRebooted
	maxTimeNodeHasRebooted := metav1.NewTime(metav1.Now().Add(safeTimeToAssumeNodeRebooted))
	//todo once we let the user config these values we need to make sure that safeTimeToAssumeNodeRebooted >> watchdog timeout
	ppr.Status.TimeAssumedRebooted = &maxTimeNodeHasRebooted
	ppr.Status.NodeBackup = node
	ppr.Status.NodeBackup.Kind = node.GetObjectKind().GroupVersionKind().Kind

	err := r.Client.Status().Update(context.Background(), ppr)
	if err != nil {
		if apiErrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		r.Log.Error(err, "failed to update status with 'node back up' and 'time to assume node has rebooted'")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PoisonPillRemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PoisonPillRemediation{}).
		Complete(r)
}

func (r *PoisonPillRemediationReconciler) initWatchdog() error {
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
func (r *PoisonPillRemediationReconciler) getMyNode() (*v1.Node, error) {
	nodeNamespacedName := types.NamespacedName{
		Namespace: "",
		Name:      myNodeName,
	}

	node := &v1.Node{}
	err := r.Get(context.TODO(), nodeNamespacedName, node)

	return node, err
}

func (r *PoisonPillRemediationReconciler) restoreNode(nodeToRestore *v1.Node) (ctrl.Result, error) {
	r.Log.Info("restoring node", "node name", nodeToRestore.Name)

	// todo we probably want to have some allowlist/denylist on which things to restore, we already had
	// a problem when we restored ovn annotations
	nodeToRestore.ResourceVersion = "" //create won't work with a non-empty value here
	taints, _ := utils.DeleteTaint(nodeToRestore.Spec.Taints, NodeUnschedulableTaint)
	nodeToRestore.Spec.Taints = taints
	nodeToRestore.Spec.Unschedulable = false
	nodeToRestore.CreationTimestamp = metav1.Now()

	//todo should we also delete conditions that made the node reach the unhealthy state?
	//I'm afraid we might cause remediation loops

	if err := r.Client.Create(context.TODO(), nodeToRestore); err != nil {
		if apiErrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}

		r.Log.Error(err, "failed to create node", "node name", nodeToRestore.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
}

// getNodeByMachine returns the node object referenced by machine
func (r *PoisonPillRemediationReconciler) getNodeFromPpr(ppr *v1alpha1.PoisonPillRemediation) (*v1.Node, error) {
	node := &v1.Node{}
	key := client.ObjectKey{
		Name:      ppr.Name,
		Namespace: "",
	}

	if err := r.ApiReader.Get(context.TODO(), key, node); err != nil {
		return nil, err
	}

	return node, nil
}

func (r *PoisonPillRemediationReconciler) markNodeAsUnschedulable(node *v1.Node) (ctrl.Result, error) {
	node.Spec.Unschedulable = true
	r.Log.Info("Marking node as unschedulable", "node name", node.Name)
	if err := r.Client.Update(context.Background(), node); err != nil {
		r.Log.Error(err, "failed to mark node as unschedulable")
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
}

//handleApiError handles the case where the api server is not responding or responding with an error
func (r *PoisonPillRemediationReconciler) handleApiError(err error) (ctrl.Result, error) {
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
		//todo consider using [m|n]hc.spec.maxUnhealthy instead of 50%
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

func (r *PoisonPillRemediationReconciler) sumPeersResponses(nodesBatchCount int, responsesChan chan poisonPill.HealthCheckResponse) (int, int, int, int) {
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

// reboot stops watchdog feeding ig watchdog exists, otherwise issuing a software reboot
func (r *PoisonPillRemediationReconciler) reboot() (ctrl.Result, error) {
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
func (r *PoisonPillRemediationReconciler) updateNodesList() error {
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
func (r *PoisonPillRemediationReconciler) getHealthStatusFromPeer(endpointIp string, results chan<- poisonPill.HealthCheckResponse) {
	url := fmt.Sprintf("%s://%s:%d/health/%s", peersProtocol, endpointIp, peersPort, myNodeName)

	resp, err := httpClient.Get(url)

	if err != nil {
		r.Log.Error(err, "failed to get health status from peer", "url", url)
		results <- -1
		return
	}

	defer func() {
		if err = resp.Body.Close(); err != nil {
			r.Log.Error(err, "failed to close health response from peer")
		}
	}()

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
func (r *PoisonPillRemediationReconciler) stopWatchdogFeeding() {
	r.Log.Info("No more food for watchdog... system will be rebooted...", "node name", myNodeName)
	shouldReboot = true
}

func (r *PoisonPillRemediationReconciler) handleDeletedNode(ppr *v1alpha1.PoisonPillRemediation) (ctrl.Result, error) {
	if ppr.Status.NodeBackup == nil {
		err := errors.New("unhealthy node doesn't exist and there's no backup node to restore")
		r.Log.Error(err, "remediation failed")
		return ctrl.Result{}, err
	}

	return r.restoreNode(ppr.Status.NodeBackup)
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
