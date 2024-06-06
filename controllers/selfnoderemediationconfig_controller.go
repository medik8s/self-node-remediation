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
	"time"

	"github.com/go-logr/logr"
	pkgerrors "github.com/pkg/errors"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/apply"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/render"
)

const (
	lastChangedAnnotationKey = "snr.medik8s.io/force-deletion-revision"
)

// SelfNodeRemediationConfigReconciler reconciles a SelfNodeRemediationConfig object
type SelfNodeRemediationConfigReconciler struct {
	client.Client
	Log                      logr.Logger
	Scheme                   *runtime.Scheme
	InstallFileFolder        string
	Namespace                string
	RebootDurationCalculator reboot.Calculator
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
	err := r.Client.Get(ctx, req.NamespacedName, config)
	//In case config is deleted (or about to be deleted) do nothing in order not to interfere with OLM delete process
	if err != nil && errors.IsNotFound(err) || err == nil && config.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if err != nil {
		logger.Error(err, "failed to fetch cr")
		return ctrl.Result{}, err
	}

	r.RebootDurationCalculator.SetConfig(config)

	if err := r.syncCerts(config); err != nil {
		logger.Error(err, "error syncing certs")
		return ctrl.Result{}, err
	}

	if err := r.syncConfigDaemonSet(ctx, config); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		logger.Error(err, "error syncing DS")
		return ctrl.Result{}, err
	}

	if err := r.setCRsEnabledStatus(ctx); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SelfNodeRemediationConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	generationChangePredicate := predicate.GenerationChangedPredicate{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}).
		Owns(&v1.DaemonSet{}, builder.WithPredicates(
			// only
			predicate.Funcs{
				// skip reconcile for status only changes
				UpdateFunc: func(ev event.UpdateEvent) bool {
					return generationChangePredicate.Update(ev)
				},
				// we want to recreate the DS in case someone deletes it
				DeleteFunc: func(_ event.DeleteEvent) bool { return true },
			}),
		).
		Complete(r)
}

func (r *SelfNodeRemediationConfigReconciler) syncConfigDaemonSet(ctx context.Context, snrConfig *selfnoderemediationv1alpha1.SelfNodeRemediationConfig) error {
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
	data.Data["EndpointHealthCheckUrl"] = snrConfig.Spec.EndpointHealthCheckUrl
	data.Data["MinPeersForRemediation"] = snrConfig.Spec.MinPeersForRemediation
	data.Data["HostPort"] = snrConfig.Spec.HostPort
	data.Data["IsSoftwareRebootEnabled"] = fmt.Sprintf("\"%t\"", snrConfig.Spec.IsSoftwareRebootEnabled)

	objs, err := render.Dir(r.InstallFileFolder, &data)
	if err != nil {
		logger.Error(err, "Fail to render config daemon manifests")
		return err
	}

	if err := r.updateDsTolerations(objs, snrConfig.Spec.CustomDsTolerations); err != nil {
		logger.Error(err, "Fail update daemonset tolerations")
		return err
	}

	// Sync DaemonSets
	for _, obj := range objs {
		if err := r.removeOldDsOnOperatorUpdate(ctx, obj.GetName(), obj.GetAnnotations()[lastChangedAnnotationKey]); err != nil {
			return err
		}
		err = r.syncK8sResource(ctx, snrConfig, obj)
		if err != nil {
			if errors.IsConflict(err) {
				logger.Info("Couldn't sync self-node-remediation daemons objects because ds is updated, retrying...")
			} else {
				logger.Error(err, "Couldn't sync self-node-remediation daemons objects")
			}
			return err
		}
	}
	return nil
}

func (r *SelfNodeRemediationConfigReconciler) syncK8sResource(ctx context.Context, cr *selfnoderemediationv1alpha1.SelfNodeRemediationConfig, in *unstructured.Unstructured) error {
	// set owner-reference only for namespaced objects
	if in.GetKind() != "ClusterRole" && in.GetKind() != "ClusterRoleBinding" {
		if err := controllerutil.SetControllerReference(cr, in, r.Scheme); err != nil {
			return err
		}
	}

	if err := apply.ApplyObject(ctx, r.Client, in); err != nil {
		return pkgerrors.Wrapf(err, "failed to apply object %v", in)
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

func (r *SelfNodeRemediationConfigReconciler) updateDsTolerations(objs []*unstructured.Unstructured, tolerations []corev1.Toleration) error {
	r.Log.Info("Updating DS tolerations")
	//Expecting to find a single DS object
	if len(objs) != 1 {
		err := fmt.Errorf("/install folder does not contain exectly one ds object")
		r.Log.Error(err, "expecting exactly one ds element in /install folder", "actual number of elements", len(objs))
		return err
	}
	if len(tolerations) == 0 {
		return nil
	}

	ds := objs[0]
	existingTolerations, _, err := unstructured.NestedSlice(ds.Object, "spec", "template", "spec", "tolerations")
	if err != nil {
		r.Log.Error(err, "error fetching tolerations from ds")
		return err
	}

	convertedTolerations, err := r.convertTolerationsToUnstructed(tolerations)
	if err != nil {
		return err
	}
	updatedTolerations := append(existingTolerations, convertedTolerations...)
	if err := unstructured.SetNestedSlice(ds.Object, updatedTolerations, "spec", "template", "spec", "tolerations"); err != nil {
		r.Log.Error(err, "failed to set tolerations")
		return err
	}

	return nil
}

func (r *SelfNodeRemediationConfigReconciler) convertTolerationsToUnstructed(tolerations []corev1.Toleration) ([]interface{}, error) {
	var convertedTolerations []interface{}
	for _, toleration := range tolerations {

		convertedToleration, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&toleration)
		if err != nil {
			r.Log.Error(err, "couldn't convert toleration to unstructured")
			return nil, err
		}
		convertedTolerations = append(convertedTolerations, convertedToleration)
	}
	return convertedTolerations, nil
}

func (r *SelfNodeRemediationConfigReconciler) removeOldDsOnOperatorUpdate(ctx context.Context, dsName string, lastVersion string) error {
	ds := &v1.DaemonSet{}
	key := types.NamespacedName{
		Namespace: r.Namespace,
		Name:      dsName,
	}

	if err := r.Client.Get(ctx, key, ds); err != nil {
		if !errors.IsNotFound(err) {
			r.Log.Error(err, "snr install/update failed error when trying to fetch old daemonset")
			return pkgerrors.Wrap(err, "unable to fetch daemon set")
		}
		r.Log.Info("snr didn't find old daemonset to be deleted")
		return nil

	}

	if lastChangedFoundVal, _ := ds.Annotations[lastChangedAnnotationKey]; lastChangedFoundVal == lastVersion {
		//ds is up-to-date this is not an update scenario
		return nil
	}

	if err := r.Client.Delete(ctx, ds); err != nil {
		r.Log.Error(err, "snr update failed could not delete old daemonset")
		return pkgerrors.Wrap(err, "unable to delete old daemon set")
	}
	r.Log.Info("snr update old daemonset deleted")
	return nil

}

func (r *SelfNodeRemediationConfigReconciler) setCRsEnabledStatus(ctx context.Context) error {
	crs := selfnoderemediationv1alpha1.SelfNodeRemediationList{}
	if err := r.List(ctx, &crs); err != nil {
		r.Log.Error(err, "Failed to list self node remediation CRs")
		return err
	}

	for _, cr := range crs.Items {
		//is disabled
		if meta.IsStatusConditionTrue(cr.Status.Conditions, string(selfnoderemediationv1alpha1.DisabledConditionType)) {
			//since config was created snr is now enabled, so we can remove the disabled status condition
			meta.RemoveStatusCondition(&cr.Status.Conditions, string(selfnoderemediationv1alpha1.DisabledConditionType))
			if err := r.Status().Update(ctx, &cr); err != nil {
				r.Log.Error(err, "Failed to update self node remediation CR status")
				return err
			}
		}
	}
	return nil
}
