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
	machineopenshiftiov1beta1 "github.com/n1r1/poison-pill-op-sdk/api/v1beta1"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	"net/http"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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
	machineNameEnvVar             = "TBD"
	maxFailuresThreshold          = 3
	peersProtocol                 = "http"
	peersPort                     = 30001
)

//var _ reconcile.Reconciler = &ReconcileMachine{}
var nodes *v1.NodeList
var errCount int
var machineName = os.Getenv(machineNameEnvVar)

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=machine.openshift.io.example.com,resources=machines/status,verbs=get;update;patch
func (r *MachineReconciler) Reconcile(request ctrl.Request) (ctrl.Result, error) {
	if request.Name != machineName {
		//we only want to be triggered by the machine this code runs on
		//later we will watch only the relevant machine
		return reconcile.Result{}, nil
	}

	machine := &machinev1beta1.Machine{}
	err := r.Get(context.TODO(), request.NamespacedName, machine)

	if err != nil {
		errCount++
		r.Log.Error(err, "Failed to retrieve machine from api-server", "error count is now", errCount)

		if errCount > maxFailuresThreshold {
			if nodes == nil {
				if err := r.updateNodesList(); err != nil {
					r.Log.Error(err, "peers list is empty and couldn't be retrieved from server")
					return reconcile.Result{}, err
				}
			}

			result, _ := r.getHealthStatusFromPeer(getRandomNodeAddress(nodes))
			r.doSomethingWithHealthResult(result)
		}

		return reconcile.Result{}, err
	}

	errCount = 0

	_ = r.updateNodesList()

	if _, exists := machine.Annotations[externalRemediationAnnotation]; exists {
		r.stopWatchdogTickle()
		return reconcile.Result{}, nil
	}

	return reconcile.Result{RequeueAfter: time.Minute}, nil
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
	url := fmt.Sprintf("%s://%s:%d/health/%s", peersProtocol, endpointIp, peersPort, machineName)
	resp, err := http.Get(url)

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
	//todo this should be random, check if address exists, etc.
	address = nodes.Items[0].Status.Addresses[0].Address
	nodes.Items = nodes.Items[1:] //remove this node from the list

	return
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&machineopenshiftiov1beta1.Machine{}).
		Complete(r)
}
