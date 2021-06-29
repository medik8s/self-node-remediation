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
	"fmt"
	"os"

	"github.com/go-logr/logr"
	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/pkg/apply"
	"github.com/medik8s/poison-pill/pkg/render"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// PoisonPillConfigReconciler reconciles a PoisonPillConfig object
type PoisonPillConfigReconciler struct {
	client.Client
	Log               logr.Logger
	Scheme            *runtime.Scheme
	InstallFileFolder string
}

//+kubebuilder:rbac:groups=poison-pill.medik8s.io,resources=poisonpillconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=poison-pill.medik8s.io,resources=poisonpillconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=poison-pill.medik8s.io,resources=poisonpillconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch;update;patch;create
//+kubebuilder:rbac:groups="security.openshift.io",resources=securitycontextconstraints,verbs=use,resourceNames=privileged
//+kubebuilder:rbac:groups=machine.openshift.io,resources=machines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=machine.openshift.io,resources=machines/status,verbs=get;update;patch

func (r *PoisonPillConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("poisonpillconfig", req.NamespacedName)

	config := &poisonpillv1alpha1.PoisonPillConfig{}
	if err := r.Client.Get(context.Background(), req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to fetch cr")
		return ctrl.Result{}, err
	}

	if err := r.syncConfigDaemonSet(config); err != nil {
		logger.Error(err, "error syncing DS")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PoisonPillConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&poisonpillv1alpha1.PoisonPillConfig{}).
		Owns(&v1.DaemonSet{}).
		Complete(r)
}

func (r *PoisonPillConfigReconciler) syncConfigDaemonSet(ppc *poisonpillv1alpha1.PoisonPillConfig) error {
	logger := r.Log.WithName("syncConfigDaemonset")
	logger.Info("Start to sync config daemonset")

	data := render.MakeRenderData()
	data.Data["Image"] = os.Getenv("POISON_PILL_IMAGE")
	data.Data["Namespace"] = ppc.Namespace

	watchdogPath := ppc.Spec.WatchdogFilePath
	if watchdogPath == "" {
		watchdogPath = "/dev/watchdog1"
	}
	data.Data["WatchdogPath"] = watchdogPath

	timeToAssumeNodeRebooted := ppc.Spec.SafeTimeToAssumeNodeRebootedSeconds
	if timeToAssumeNodeRebooted == 0 {
		timeToAssumeNodeRebooted = 180
	}
	data.Data["TimeToAssumeNodeRebooted"] = fmt.Sprintf("\"%d\"", timeToAssumeNodeRebooted)

	objs, err := render.RenderDir(r.InstallFileFolder, &data)
	if err != nil {
		logger.Error(err, "Fail to render config daemon manifests")
		return err
	}
	// Sync DaemonSets
	for _, obj := range objs {
		err = r.syncK8sResource(ppc, obj)
		if err != nil {
			logger.Error(err, "Couldn't sync poison-pill daemons objects")
			return err
		}
	}
	return nil
}

func (r *PoisonPillConfigReconciler) syncK8sResource(cr *poisonpillv1alpha1.PoisonPillConfig, in *unstructured.Unstructured) error {
	// set owner-reference only for namespaced objects
	if in.GetKind() != "ClusterRole" && in.GetKind() != "ClusterRoleBinding" {
		if err := controllerutil.SetControllerReference(cr, in, r.Scheme); err != nil {
			return err
		}
	}

	if err := apply.ApplyObject(context.TODO(), r.Client, in); err != nil {
		return fmt.Errorf("failed to apply object %v with err: %v", in, err)
	}
	return nil
}
