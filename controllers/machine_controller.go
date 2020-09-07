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
	"errors"
	"fmt"
	poisonPill "github.com/n1r1/poison-pill/api"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
)

const (
	externalRemediationAnnotation = "host.metal3.io/external-remediation"
	nodeNameEnvVar                = "MY_NODE_NAME"
	maxFailuresThreshold          = 3
	peersProtocol                 = "http"
	peersPort                     = 30001
	machineAnnotationKey          = "machine.openshift.io/machine"
	reconcileInterval             = 15 * time.Second
	apiServerTimeout              = 5 * time.Second
	peerTimeout                   = 10 * time.Second
)

//var _ reconcile.Reconciler = &ReconcileMachine{}
var nodes *v1.NodeList
var errCount int
var myNodeName = os.Getenv(nodeNameEnvVar)
var myMachineName string
var lastReconcileTime time.Time
var httpClient = &http.Client{
	Timeout: peerTimeout,
}

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	ApiReader client.Reader
}

// updates machineName to the machine associated with the myNodeName
func (r *MachineReconciler) getMachineName() (ctrl.Result, error) {
	r.Log.Info("Determining machine name ...")
	nodeNamespacedName := types.NamespacedName{
		Namespace: "",
		Name:      myNodeName,
	}

	node := &v1.Node{}
	if err := r.Get(context.TODO(), nodeNamespacedName, node); err != nil {
		r.Log.Error(err, "Failed to retrieve node object", "node name", myNodeName)
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	if node.Annotations == nil {
		err := errors.New("No annotations on node. Can't determine machine name")
		r.Log.Error(err, "")
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	machine, machineExists := node.Annotations[machineAnnotationKey]

	if !machineExists {
		err := errors.New("machine annotation is not present on the node. can't determine machine name")
		r.Log.Error(err, "")
		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	myMachineName = machine
	r.Log.Info("Detected machine name", "my machine name", myMachineName)
	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
}

// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines/status,verbs=get;update;patch
func (r *MachineReconciler) Reconcile(request ctrl.Request) (ctrl.Result, error) {
	// check what's the machine name I'm running on, only if it's not already known
	if myMachineName == "" {
		return r.getMachineName()
	}

	//we only want to reconcile the machine this pod runs on
	request.Name = myMachineName

	minTimeToReconcile := lastReconcileTime.Add(reconcileInterval)
	if !time.Now().After(minTimeToReconcile) {
		return ctrl.Result{RequeueAfter: minTimeToReconcile.Sub(time.Now())}, nil
	}

	r.Log.Info("Reconciling...")
	lastReconcileTime = time.Now()

	machine := &machinev1beta1.Machine{}

	//define context with timeout, otherwise this blocks forever
	ctx, cancelFunc := context.WithTimeout(context.TODO(), apiServerTimeout)
	defer cancelFunc()

	//we use ApiReader as it doesn't use the cache. Otherwise, the cache returns the object
	//event though the api server is not available
	err := r.ApiReader.Get(ctx, request.NamespacedName, machine)

	if err != nil {
		errCount++
		r.Log.Error(err, "Failed to retrieve machine from api-server", "error count is now", errCount)

		if errCount > maxFailuresThreshold {
			r.Log.Info("Error count exceeds threshold, trying to ask other nodes if I'm healthy")
			if nodes == nil || len(nodes.Items) == 0 {
				if err := r.updateNodesList(); err != nil {
					r.Log.Error(err, "peers list is empty and couldn't be retrieved from server")
					return ctrl.Result{RequeueAfter: reconcileInterval}, err
				}
			}

			result, err := r.getHealthStatusFromPeer(getRandomNodeAddress(nodes))
			if err != nil {
				if len(nodes.Items) == 1 {
					//we got an error from the last node in the list
					r.Log.Error(err, "Failed to get health status from the last peer in the list. Assuming unhealthy")
					r.stopWatchdogTickle()
				}
				return ctrl.Result{RequeueAfter: reconcileInterval}, err
			}

			r.Log.Info("Got response from peer", "response", result)
			r.doSomethingWithHealthResult(result)
		}

		return ctrl.Result{RequeueAfter: reconcileInterval}, err
	}

	errCount = 0

	_ = r.updateNodesList()

	if _, exists := machine.Annotations[externalRemediationAnnotation]; exists {
		r.Log.Info("Found external remediation annotation. Machine is unhealthy")
		r.stopWatchdogTickle()
		return ctrl.Result{RequeueAfter: reconcileInterval}, nil
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
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
		r.stopWatchdogTickle()
		break
	case poisonPill.Healthy:
		r.Log.Info("Peer told me I'm healthy. Resetting error count")
		errCount = 0
		break
	case poisonPill.ApiError:
		break
	default:
		r.Log.Error(errors.New("got unexpected value from peer while trying to retrieve health status"), "Received value", healthStatus)
	}
}

//this will cause reboot
func (r *MachineReconciler) stopWatchdogTickle() {
	r.Log.Info("Rebooting...")
	//todo implement
}

func getRandomNodeAddress(nodes *v1.NodeList) (address string) {
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

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&machinev1beta1.Machine{}).
		Complete(r)
}
