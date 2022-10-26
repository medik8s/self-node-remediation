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
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/apply"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/render"
)

// SelfNodeRemediationConfigReconciler reconciles a SelfNodeRemediationConfig object
type SelfNodeRemediationConfigReconciler struct {
	client.Client
	Log               logr.Logger
	Scheme            *runtime.Scheme
	InstallFileFolder string
	DefaultPpcCreator func(c client.Client) error
	Namespace         string
}

//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch;update;patch;create;delete
//+kubebuilder:rbac:groups="apps",resources=daemonsets/finalizers,verbs=update
//+kubebuilder:rbac:groups="security.openshift.io",resources=securitycontextconstraints,verbs=use,resourceNames=privileged
//+kubebuilder:rbac:groups=machine.openshift.io,resources=machines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=machine.openshift.io,resources=machines/status,verbs=get;update;patch

func (r *SelfNodeRemediationConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("selfnoderemediationconfig", req.NamespacedName)

	if req.Name != selfnoderemediationv1alpha1.ConfigCRName || req.Namespace != r.Namespace {
		logger.Info(fmt.Sprintf("ignoring selfnoderemediationconfig CRs that are not named '%s' or not in the namespace of the operator: '%s'",
			selfnoderemediationv1alpha1.ConfigCRName, r.Namespace))
		return ctrl.Result{}, nil
	}

	config := &selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}
	if err := r.Client.Get(context.Background(), req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			err := r.DefaultPpcCreator(r.Client)
			return ctrl.Result{}, err
		}
		logger.Error(err, "failed to fetch cr")
		return ctrl.Result{}, err
	}

	if err := r.syncCerts(config); err != nil {
		logger.Error(err, "error syncing certs")
		return ctrl.Result{}, err
	}

	if err := r.syncConfigDaemonSet(config); err != nil {
		logger.Error(err, "error syncing DS")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SelfNodeRemediationConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}).
		Owns(&v1.DaemonSet{}).
		Complete(r)
}

func (r *SelfNodeRemediationConfigReconciler) syncConfigDaemonSet(snrConfig *selfnoderemediationv1alpha1.SelfNodeRemediationConfig) error {
	logger := r.Log.WithName("syncConfigDaemonset")
	logger.Info("Start to sync config daemonset")

	data := render.MakeRenderData()
	data.Data["Image"] = os.Getenv("SELF_NODE_REMEDIATION_IMAGE")
	data.Data["Namespace"] = snrConfig.Namespace

	watchdogPath := snrConfig.Spec.WatchdogFilePath
	if watchdogPath == "" {
		watchdogPath = "/dev/watchdog"
	}
	data.Data["WatchdogPath"] = watchdogPath

	data.Data["PeerApiServerTimeout"] = snrConfig.Spec.PeerApiServerTimeout.Nanoseconds()
	data.Data["ApiCheckInterval"] = snrConfig.Spec.ApiCheckInterval.Nanoseconds()
	data.Data["PeerUpdateInterval"] = snrConfig.Spec.PeerUpdateInterval.Nanoseconds()
	data.Data["ApiServerTimeout"] = snrConfig.Spec.ApiServerTimeout.Nanoseconds()
	data.Data["PeerDialTimeout"] = snrConfig.Spec.PeerDialTimeout.Nanoseconds()
	data.Data["PeerRequestTimeout"] = snrConfig.Spec.PeerRequestTimeout.Nanoseconds()
	data.Data["MaxApiErrorThreshold"] = snrConfig.Spec.MaxApiErrorThreshold
	data.Data["EndPointHealthCheckUrl"] = snrConfig.Spec.EndPointHealthCheckUrl

	timeToAssumeNodeRebooted := snrConfig.Spec.SafeTimeToAssumeNodeRebootedSeconds
	if timeToAssumeNodeRebooted == 0 {
		timeToAssumeNodeRebooted = 180
	}
	data.Data["TimeToAssumeNodeRebooted"] = fmt.Sprintf("\"%d\"", timeToAssumeNodeRebooted)

	data.Data["IsSoftwareRebootEnabled"] = fmt.Sprintf("\"%t\"", snrConfig.Spec.IsSoftwareRebootEnabled)

	objs, err := render.Dir(r.InstallFileFolder, &data)
	if err != nil {
		logger.Error(err, "Fail to render config daemon manifests")
		return err
	}
	// Sync DaemonSets
	for _, obj := range objs {
		err = r.syncK8sResource(snrConfig, obj)
		if err != nil {
			logger.Error(err, "Couldn't sync self-node-remediation daemons objects")
			return err
		}
	}
	return nil
}

func (r *SelfNodeRemediationConfigReconciler) syncK8sResource(cr *selfnoderemediationv1alpha1.SelfNodeRemediationConfig, in *unstructured.Unstructured) error {
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

func (r *SelfNodeRemediationConfigReconciler) syncCerts(cr *selfnoderemediationv1alpha1.SelfNodeRemediationConfig) error {

	r.Log.Info("Syncing certs")
	// check if certs exists already
	st := certificates.NewSecretCertStorage(r.Client, r.Log.WithName("SecretCertStorage"), cr.Namespace)
	pem, _, _, err := st.GetCerts()
	if err != nil && !errors.IsNotFound(err) {
		r.Log.Error(err, "Failed to get cert secret")
		return err
	}
	if pem != nil {
		r.Log.Info("Cert secret already exists")
		// TODO check validity: if expired, create new, restart daemonset!
		return nil
	}
	// create certs
	r.Log.Info("Creating new certs")
	ca, cert, key, err := certificates.CreateCerts()
	if err != nil {
		r.Log.Error(err, "Failed to create certs")
		return err
	}
	// store certs
	r.Log.Info("Storing certs in new secret")
	err = st.StoreCerts(ca, cert, key)
	if err != nil {
		r.Log.Error(err, "Failed to store certs in secret")
		return err
	}
	return nil
}
