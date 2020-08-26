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
	"github.com/prometheus/common/log"
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
	reconcileInterval             = 30 * time.Second
)

//var _ reconcile.Reconciler = &ReconcileMachine{}
var nodes *v1.NodeList
var errCount int
var nodeName = os.Getenv(nodeNameEnvVar)
var myMachineName string

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	ApiReader client.Reader
}

// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines/status,verbs=get;update;patch
func (r *MachineReconciler) Reconcile(request ctrl.Request) (ctrl.Result, error) {
	r.Log.Info("Reconciling...")

	// check what's the machine name I'm running on, only if it's not already known
	if myMachineName == "" {
		r.Log.Info("Determining machine name ...")
		nodeNamespacedName := types.NamespacedName{
			Namespace: "",
			Name:      nodeName,
		}

		node := &v1.Node{}
		if err := r.Get(context.TODO(), nodeNamespacedName, node); err != nil {
			r.Log.Error(err, "Failed to retrieve node object", "node name", nodeName)
			return ctrl.Result{RequeueAfter: reconcileInterval}, err
		}

		if node.Annotations == nil {
			err := errors.New("No annotations on node. Can't determine machine name")
			r.Log.Error(err,"")
			return ctrl.Result{RequeueAfter: reconcileInterval}, err
		}

		if machine, machineExists := node.Annotations[machineAnnotationKey]; machineExists {
			myMachineName = machine
		} else {
			err := errors.New("machine annotation is not present on the node. can't determine machine name")
			r.Log.Error(err, "")
			return ctrl.Result{RequeueAfter: reconcileInterval}, err
		}
	}

	//we only want to reconcile the machine this pod runs on
	request.Name = myMachineName
	//if request.Name != myMachineName {
	//	//we only want to be triggered by the machine this code runs on
	//	//later we will watch only the relevant machine
	//	return ctrl.Result{RequeueAfter: reconcileInterval}, nil
	//}

	r.Log.Info("Fetching machine...")
	machine := &machinev1beta1.Machine{}
	ctx, cancelFunc := context.WithTimeout(context.TODO(), time.Second*5) //todo check for err
	defer cancelFunc()
	err := r.ApiReader.Get(ctx, request.NamespacedName, machine)

	if err != nil {
		r.Log.Info("Error...")
		errCount++
		r.Log.Error(err, "Failed to retrieve machine from api-server", "error count is now", errCount)

		if errCount > maxFailuresThreshold {
			log.Info("Errors count exceeds threshold, trying to ask other nodes if I'm healthy")
			if nodes == nil {
				if err := r.updateNodesList(); err != nil {
					r.Log.Error(err, "peers list is empty and couldn't be retrieved from server")
					return ctrl.Result{RequeueAfter: reconcileInterval}, err
				}
			}

			result, err := r.getHealthStatusFromPeer(getRandomNodeAddress(nodes))
			if err != nil {
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
	return nil
}

func (r *MachineReconciler) getHealthStatusFromPeer(endpointIp string) (poisonPill.HealthCheckResponse, error) {
	url := fmt.Sprintf("%s://%s:%d/health/%s", peersProtocol, endpointIp, peersPort, myMachineName)

	//todo init only once
	c := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := c.Get(url)

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
		//do nothing?
		break
	case poisonPill.ApiError:
		//todo ?
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
	address = nodes.Items[nodeIndex].Status.Addresses[0].Address
	nodes.Items = nodes.Items[1:] //remove this node from the list

	return
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&machinev1beta1.Machine{}).
		Complete(r)
}
