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
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

const (
	SNRFinalizer         = "self-node-remediation.medik8s.io/snr-finalizer"
	nhcTimeOutAnnotation = "remediation.medik8s.io/nhc-timed-out"
)

var (
	NodeUnschedulableTaint = &v1.Taint{
		Key:    "node.kubernetes.io/unschedulable",
		Effect: v1.TaintEffectNoSchedule,
	}

	NodeNoExecuteTaint = &v1.Taint{
		Key:    "medik8s.io/remediation",
		Value:  "self-node-remediation",
		Effect: v1.TaintEffectNoExecute,
	}

	OutOfServiceTaint = &v1.Taint{
		Key:    "node.kubernetes.io/out-of-service",
		Value:  "nodeshutdown",
		Effect: v1.TaintEffectNoExecute,
	}

	lastSeenSnrNamespace  string
	wasLastSeenSnrMachine bool
)

type processingChangeReason string

const (
	remediationStarted         processingChangeReason = "RemediationStarted"
	remediationTerminatedByNHC processingChangeReason = "RemediationStoppedByNHC"
	remediationFinished        processingChangeReason = "RemediationFinished"
)

type remediationPhase string

const (
	fencingStartedPhase     remediationPhase = "Fencing-Started"
	preRebootCompletedPhase remediationPhase = "Pre-Reboot-Completed"
	rebootCompletedPhase    remediationPhase = "Reboot-Completed"
	fencingCompletedPhase   remediationPhase = "Fencing-Completed"
	unknownPhase            remediationPhase = "Unknown"
)

type UnreconcilableError struct {
	msg string
}

func (e *UnreconcilableError) Error() string {
	return e.msg
}

// GetLastSeenSnrNamespace returns the namespace of the last reconciled SNR
func (r *SelfNodeRemediationReconciler) GetLastSeenSnrNamespace() string {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return lastSeenSnrNamespace
}

// WasLastSeenSnrMachine returns the a boolean indicating if the last reconcile SNR
// was pointing an unhealthy machine or a node
func (r *SelfNodeRemediationReconciler) WasLastSeenSnrMachine() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return wasLastSeenSnrMachine
}

// SelfNodeRemediationReconciler reconciles a SelfNodeRemediation object
type SelfNodeRemediationReconciler struct {
	client.Client
	Log logr.Logger
	//logger is a logger that holds the CR name being reconciled
	logger     logr.Logger
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	Rebooter   reboot.Rebooter
	MyNodeName string
	mutex      sync.Mutex
	//we need to restore the node only after the cluster realized it can reschecudle the affected workloads
	//as of writing this lines, kubernetes will check for pods with non-existent node once in 20s, and allows
	//40s of grace period for the node to reappear before it deletes the pods.
	//see here: https://github.com/kubernetes/kubernetes/blob/7a0638da76cb9843def65708b661d2c6aa58ed5a/pkg/controller/podgc/gc_controller.go#L43-L47
	RestoreNodeAfter time.Duration
}

// SetupWithManager sets up the controller with the Manager.
func (r *SelfNodeRemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SelfNodeRemediation{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;delete;deletecollection
//+kubebuilder:rbac:groups=storage.k8s.io,resources=volumeattachments,verbs=get;list;watch;update;delete;deletecollection
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates/finalizers,verbs=update
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediations/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=machine.openshift.io,resources=machines,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=list;get;watch

func (r *SelfNodeRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (returnResult ctrl.Result, returnErr error) {
	r.logger = r.Log.WithValues("selfnoderemediation", req.NamespacedName)

	snr := &v1alpha1.SelfNodeRemediation{}
	if err := r.Get(ctx, req.NamespacedName, snr); err != nil {
		if apiErrors.IsNotFound(err) {
			// SNR is deleted, stop reconciling
			r.logger.Info("SNR already deleted")
			return ctrl.Result{}, nil
		}
		r.logger.Error(err, "failed to get SNR")
		return ctrl.Result{}, err
	}

	snrOrg := snr.DeepCopy()
	defer func() {
		if patchErr := r.patchSnrStatus(ctx, snr, snrOrg); patchErr != nil {
			if returnErr == nil {
				returnErr = patchErr
			} else {
				returnErr = utilerrors.NewAggregate([]error{patchErr, returnErr})
			}
		}
	}()

	if r.isStoppedByNHC(snr) {
		r.logger.Info("SNR remediation was stopped by Node Healthcheck")
		return ctrl.Result{}, r.updateSnrProcessingCondition(remediationTerminatedByNHC, snr)
	}

	if r.getPhase(snr) != fencingCompletedPhase {
		if err := r.updateSnrProcessingCondition(remediationStarted, snr); err != nil {
			return ctrl.Result{}, err
		}
	}

	r.mutex.Lock()
	lastSeenSnrNamespace = req.Namespace
	r.mutex.Unlock()

	result := ctrl.Result{}
	var err error

	switch snr.Spec.RemediationStrategy {
	case v1alpha1.ResourceDeletionRemediationStrategy:
		result, err = r.remediateWithResourceDeletion(snr)
	case v1alpha1.OutOfServiceTaintRemediationStrategy:
		result, err = r.remediateWithOutOfServiceTaint(snr)
	default:
		//this should never happen since we enforce valid values with kubebuilder
		err := errors.New("unsupported remediation strategy")
		r.logger.Error(err, "Encountered unsupported remediation strategy. Please check template spec", "strategy", snr.Spec.RemediationStrategy)
	}

	return result, r.updateSnrStatusLastError(snr, err)
}

func (r *SelfNodeRemediationReconciler) updateSnrProcessingCondition(reason processingChangeReason, snr *v1alpha1.SelfNodeRemediation) error {
	var conditionStatus metav1.ConditionStatus
	switch reason {
	case remediationStarted:
		conditionStatus = metav1.ConditionTrue
	case remediationFinished, remediationTerminatedByNHC:
		conditionStatus = metav1.ConditionFalse
	default:
		err := fmt.Errorf("unkown processingChangeReason:%s", reason)
		r.Log.Error(err, "couldn't update snr processing condition")
		return err
	}

	if meta.IsStatusConditionPresentAndEqual(snr.Status.Conditions, v1alpha1.SnrConditionProcessing, conditionStatus) {
		return nil
	}

	meta.SetStatusCondition(&snr.Status.Conditions, metav1.Condition{
		Type:   v1alpha1.SnrConditionProcessing,
		Status: conditionStatus,
		Reason: string(reason),
	})

	return nil

}

// Note: this method is activated automatically at the end of reconcile, and it shouldn't be called explicitly
func (r *SelfNodeRemediationReconciler) patchSnrStatus(ctx context.Context, changed, org *v1alpha1.SelfNodeRemediation) error {
	if err := r.Client.Status().Patch(ctx, changed, client.MergeFrom(org)); err != nil {
		r.logger.Error(err, "failed to patch SNR status")
		return err
	}
	return nil
}

func (r *SelfNodeRemediationReconciler) getPhase(snr *v1alpha1.SelfNodeRemediation) remediationPhase {
	if snr.Status.Phase == nil {
		return fencingStartedPhase
	}
	phase := remediationPhase(*snr.Status.Phase)
	switch phase {
	case preRebootCompletedPhase, rebootCompletedPhase, fencingCompletedPhase:
		return phase
	default:
		return unknownPhase
	}
}

func (r *SelfNodeRemediationReconciler) remediateWithResourceDeletion(snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	return r.remediateWithResourceRemoval(snr, r.deleteResourcesWrapper)
}

// deleteResourcesWrapper returns a 'zero' time and nil if it completes to delete node resources successfully
// if not, it will return a 'zero' time and non-nil error, which means exponential backoff is triggered
// SelfNodeRemediation is only used in order to match method signature required by remediateWithResourceRemoval
func (r *SelfNodeRemediationReconciler) deleteResourcesWrapper(node *v1.Node, _ *v1alpha1.SelfNodeRemediation) (time.Duration, error) {
	return 0, r.deleteResources(node)
}

func (r *SelfNodeRemediationReconciler) deleteResources(node *v1.Node) error {
	//fence
	zero := int64(0)
	backgroundDeletePolicy := metav1.DeletePropagationBackground

	deleteOptions := &client.DeleteAllOfOptions{
		ListOptions: client.ListOptions{
			FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}),
			Namespace:     "",
			Limit:         0,
		},
		DeleteOptions: client.DeleteOptions{
			GracePeriodSeconds: &zero,
			PropagationPolicy:  &backgroundDeletePolicy,
		},
	}

	namespaces := v1.NamespaceList{}
	if err := r.Client.List(context.Background(), &namespaces); err != nil {
		r.logger.Error(err, "failed to list namespaces", err)
		return err
	}

	r.logger.Info("starting to delete node resources", "node name", node.Name)

	pod := &v1.Pod{}
	for _, ns := range namespaces.Items {
		deleteOptions.Namespace = ns.Name
		err := r.Client.DeleteAllOf(context.Background(), pod, deleteOptions)
		if err != nil {
			r.logger.Error(err, "failed to delete pods of unhealthy node", "namespace", ns.Name)
			return err
		}
	}

	volumeAttachments := &storagev1.VolumeAttachmentList{}
	if err := r.Client.List(context.Background(), volumeAttachments); err != nil {
		r.logger.Error(err, "failed to get volumeAttachments list")
		return err
	}
	forceDeleteOption := &client.DeleteOptions{
		GracePeriodSeconds: &zero,
	}
	for _, va := range volumeAttachments.Items {
		if va.Spec.NodeName == node.Name {
			err := r.Client.Delete(context.Background(), &va, forceDeleteOption)
			if err != nil {
				r.logger.Error(err, "failed to delete volumeAttachment", "name", va.Name)
				return err
			}
		}
	}

	r.logger.Info("done deleting node resources", "node name", node.Name)

	return nil
}

func (r *SelfNodeRemediationReconciler) remediateWithOutOfServiceTaint(snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	return r.remediateWithResourceRemoval(snr, r.useOutOfServiceTaint)
}

func (r *SelfNodeRemediationReconciler) useOutOfServiceTaint(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (time.Duration, error) {
	if err := r.addOutOfServiceTaint(node); err != nil {
		return 0, err
	}

	// We can not control to delete node resources by the "out-of-service" taint
	// So timer is used to avoid to keep waiting to complete
	if !r.isResourceDeletionCompleted(node) {
		isExpired, timeLeft := r.isResourceDeletionExpired(snr)
		if !isExpired {
			return timeLeft, nil
		}
		// if the timer is expired, exponential backoff is triggered
		return 0, errors.New("Not ready to delete out-of-service taint")
	}

	if err := r.removeOutOfServiceTaint(node); err != nil {
		return 0, err
	}

	return 0, nil
}

type removeNodeResources func(*v1.Node, *v1alpha1.SelfNodeRemediation) (time.Duration, error)

func (r *SelfNodeRemediationReconciler) remediateWithResourceRemoval(snr *v1alpha1.SelfNodeRemediation, rmNodeResources removeNodeResources) (ctrl.Result, error) {
	node, err := r.getNodeFromSnr(snr)
	if err != nil {
		r.logger.Error(err, "failed to get node", "node name", snr.Name)
		return ctrl.Result{}, err
	}

	result := ctrl.Result{}
	phase := r.getPhase(snr)
	switch phase {
	case fencingStartedPhase:
		result, err = r.handleFencingStartedPhase(node, snr)
	case preRebootCompletedPhase:
		result, err = r.handlePreRebootCompletedPhase(node, snr)
	case rebootCompletedPhase:
		result, err = r.handleRebootCompletedPhase(node, snr, rmNodeResources)
	case fencingCompletedPhase:
		result, err = r.handleFencingCompletedPhase(node, snr)
	default:
		//this should never happen since we enforce valid values with kubebuilder
		err = errors.New("unknown phase")
		r.logger.Error(err, "Undefined unknown phase", "phase", phase)
	}
	return result, err
}

func (r *SelfNodeRemediationReconciler) handleFencingStartedPhase(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	return r.prepareReboot(node, snr)
}

func (r *SelfNodeRemediationReconciler) prepareReboot(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	r.logger.Info("pre-reboot not completed yet, prepare for rebooting")
	if !r.isNodeRebootCapable(node) {
		//use err to trigger exponential backoff
		return ctrl.Result{}, errors.New("Node is not capable to reboot itself")
	}

	if !controllerutil.ContainsFinalizer(snr, SNRFinalizer) {
		return r.addFinalizer(snr)
	}

	if err := r.addNoExecuteTaint(node); err != nil {
		return ctrl.Result{}, err
	}

	if !node.Spec.Unschedulable || !utils.TaintExists(node.Spec.Taints, NodeUnschedulableTaint) {
		return r.markNodeAsUnschedulable(node)
	}

	if snr.Status.TimeAssumedRebooted.IsZero() {
		//todo this also updates the node but we don't need it
		return r.updateSnrStatus(node, snr)
	}

	preRebootCompleted := string(preRebootCompletedPhase)
	snr.Status.Phase = &preRebootCompleted

	return ctrl.Result{}, nil
}

func (r *SelfNodeRemediationReconciler) handlePreRebootCompletedPhase(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	return r.rebootNode(node, snr)
}

func (r *SelfNodeRemediationReconciler) rebootNode(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	r.logger.Info("node reboot not completed yet, start rebooting")
	if r.MyNodeName == node.Name {
		// we have a problem on this node, reboot!
		return r.rebootIfNeeded(snr)
	}

	wasRebooted, timeLeft := r.wasNodeRebooted(snr)
	if !wasRebooted {
		return ctrl.Result{RequeueAfter: timeLeft}, nil
	}

	r.logger.Info("TimeAssumedRebooted is old. The unhealthy node assumed to been rebooted", "node name", node.Name)

	rebootCompleted := string(rebootCompletedPhase)
	snr.Status.Phase = &rebootCompleted

	return ctrl.Result{}, nil
}

func (r *SelfNodeRemediationReconciler) handleRebootCompletedPhase(node *v1.Node, snr *v1alpha1.SelfNodeRemediation, rmNodeResources removeNodeResources) (ctrl.Result, error) {
	// if err is non-nil, exponential backoff is triggred
	// if err is nil and waitTime is not a 'zero' time, wait for waitTime seconds to remove node resources
	if waitTime, err := rmNodeResources(node, snr); err != nil {
		return ctrl.Result{}, err
	} else if waitTime != 0 {
		return ctrl.Result{RequeueAfter: waitTime}, nil
	}

	fencingCompleted := string(fencingCompletedPhase)
	snr.Status.Phase = &fencingCompleted

	return ctrl.Result{}, r.updateSnrProcessingCondition(remediationFinished, snr)
}

func (r *SelfNodeRemediationReconciler) handleFencingCompletedPhase(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	result := ctrl.Result{}
	var err error

	if snr.DeletionTimestamp != nil {
		result, err = r.recoverNode(node, snr)
	}

	return result, err
}

func (r *SelfNodeRemediationReconciler) recoverNode(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	r.logger.Info("fencing completed, cleaning up")
	if node.Spec.Unschedulable {
		node.Spec.Unschedulable = false
		if err := r.Client.Update(context.Background(), node); err != nil {
			if apiErrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			r.logger.Error(err, "failed to mark node as schedulable")
			return ctrl.Result{}, err
		}
	}

	// wait until NoSchedulable taint was removed
	if utils.TaintExists(node.Spec.Taints, NodeUnschedulableTaint) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	if err := r.removeNoExecuteTaint(node); err != nil {
		return ctrl.Result{}, err
	}

	if controllerutil.ContainsFinalizer(snr, SNRFinalizer) {
		if err := r.removeFinalizer(snr); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// rebootIfNeeded reboots the node if no reboot was performed so far
func (r *SelfNodeRemediationReconciler) rebootIfNeeded(snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	shouldAvoidReboot, err := r.didIRebootMyself(snr)
	if err != nil {
		return ctrl.Result{}, err
	}

	if shouldAvoidReboot {
		//node already rebooted once during this SNR lifecycle, no need for additional reboot
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: reboot.TimeToAssumeRebootHasStarted}, r.Rebooter.Reboot()
}

// wasNodeRebooted returns true if the node assumed to been rebooted.
// if not, it will also return the remaining time for that to happen
func (r *SelfNodeRemediationReconciler) wasNodeRebooted(snr *v1alpha1.SelfNodeRemediation) (bool, time.Duration) {
	maxNodeRebootTime := snr.Status.TimeAssumedRebooted

	if maxNodeRebootTime.After(time.Now()) {
		return false, maxNodeRebootTime.Sub(time.Now()) + time.Second
	}

	return true, 0
}

// didIRebootMyself returns true if system uptime is less than the time from SNR creation timestamp
// which means that the host was already rebooted (at least) once during this SNR lifecycle
func (r *SelfNodeRemediationReconciler) didIRebootMyself(snr *v1alpha1.SelfNodeRemediation) (bool, error) {
	uptime, err := utils.GetLinuxUptime()
	if err != nil {
		r.logger.Error(err, "failed to get node's uptime")
		return false, err
	}

	return uptime < time.Since(snr.CreationTimestamp.Time), nil
}

// isNodeRebootCapable checks if the node is capable to reboot itself when it becomes unhealthy
// this boils down to check if it has an assigned self node remediation pod, and the reboot-capable annotation
func (r *SelfNodeRemediationReconciler) isNodeRebootCapable(node *v1.Node) bool {
	//make sure that the unhealthy node has self node remediation pod on it which can reboot it
	if _, err := utils.GetSelfNodeRemediationAgentPod(node.Name, r.Client); err != nil {
		r.logger.Error(err, "failed to get self node remediation agent pod resource")
		return false
	}

	//if the unhealthy node has the self node remediation agent pod, but the is-reboot-capable annotation is unknown/false/doesn't exist
	//the node might not reboot, and we might end up in deleting a running node
	if node.Annotations == nil || node.Annotations[utils.IsRebootCapableAnnotation] != "true" {
		annVal := ""
		if node.Annotations != nil {
			annVal = node.Annotations[utils.IsRebootCapableAnnotation]
		}

		r.logger.Error(errors.New("node's isRebootCapable annotation is not `true`, which means the node might not reboot when we'll delete the node. Skipping remediation"),
			"", "annotation value", annVal)
		return false
	}

	return true
}

func (r *SelfNodeRemediationReconciler) removeFinalizer(snr *v1alpha1.SelfNodeRemediation) error {
	controllerutil.RemoveFinalizer(snr, SNRFinalizer)
	if err := r.Client.Update(context.Background(), snr); err != nil {
		if apiErrors.IsConflict(err) {
			//we don't need to log anything as conflict is expected, but we do want to return an err
			//to trigger a requeue
			return err
		}
		r.logger.Error(err, "failed to remove finalizer from snr")
		return err
	}
	r.logger.Info("finalizer removed")
	return nil
}

func (r *SelfNodeRemediationReconciler) addFinalizer(snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	if !snr.DeletionTimestamp.IsZero() {
		//snr is going to be deleted before we started any remediation action, so taking no-op
		//otherwise we continue the remediation even if the deletionTimestamp is not zero
		r.logger.Info("snr is about to be deleted, which means the resource is healthy again. taking no-op")
		return ctrl.Result{}, nil
	}

	controllerutil.AddFinalizer(snr, SNRFinalizer)
	if err := r.Client.Update(context.Background(), snr); err != nil {
		if apiErrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		r.logger.Error(err, "failed to add finalizer to snr")
		return ctrl.Result{}, err
	}
	r.logger.Info("finalizer added")
	return ctrl.Result{Requeue: true}, nil
}

// returns the lastHeartbeatTime of the first condition, if exists. Otherwise returns the zero value
func (r *SelfNodeRemediationReconciler) getLastHeartbeatTime(node *v1.Node) time.Time {
	var lastHeartbeat metav1.Time
	if node.Status.Conditions != nil && len(node.Status.Conditions) > 0 {
		lastHeartbeat = node.Status.Conditions[0].LastHeartbeatTime
	}
	return lastHeartbeat.Time
}

func (r *SelfNodeRemediationReconciler) getReadyCond(node *v1.Node) *v1.NodeCondition {
	for _, cond := range node.Status.Conditions {
		if cond.Type == v1.NodeReady {
			return &cond
		}
	}
	return nil
}

func (r *SelfNodeRemediationReconciler) updateSnrStatus(node *v1.Node, snr *v1alpha1.SelfNodeRemediation) (ctrl.Result, error) {
	r.logger.Info("updating snr with node backup and updating time to assume node has been rebooted", "node name", node.Name)
	//we assume the unhealthy node will be rebooted by maxTimeNodeHasRebooted
	maxTimeNodeHasRebooted := metav1.NewTime(metav1.Now().Add(r.Rebooter.GetTimeToAssumeNodeRebooted()))
	snr.Status.TimeAssumedRebooted = &maxTimeNodeHasRebooted

	err := r.Client.Status().Update(context.Background(), snr)
	if err != nil {
		if apiErrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		r.logger.Error(err, "failed to update status with 'node back up' and 'time to assume node has rebooted'")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// getNodeFromSnr returns the unhealthy node reported in the given snr
func (r *SelfNodeRemediationReconciler) getNodeFromSnr(snr *v1alpha1.SelfNodeRemediation) (*v1.Node, error) {
	//SNR could be created by either machine based controller (e.g. MHC) or
	//by a node based controller (e.g. NHC). This assumes that machine based controller
	//will create the snr with machine owner reference

	for _, ownerRef := range snr.OwnerReferences {
		if ownerRef.Kind == "Machine" {
			r.mutex.Lock()
			wasLastSeenSnrMachine = true
			r.mutex.Unlock()
			return r.getNodeFromMachine(ownerRef, snr.Namespace)
		}
	}

	//since we didn't find a machine owner ref, we assume that snr name is the unhealthy node name
	node := &v1.Node{}
	key := client.ObjectKey{
		Name:      snr.Name,
		Namespace: "",
	}

	if err := r.Get(context.TODO(), key, node); err != nil {
		return nil, err
	}

	return node, nil
}

func (r *SelfNodeRemediationReconciler) getNodeFromMachine(ref metav1.OwnerReference, ns string) (*v1.Node, error) {
	machine := &machinev1beta1.Machine{}
	machineKey := client.ObjectKey{
		Name:      ref.Name,
		Namespace: ns,
	}

	if err := r.Client.Get(context.Background(), machineKey, machine); err != nil {
		r.logger.Error(err, "failed to get machine from SelfNodeRemediation CR owner ref",
			"machine name", machineKey.Name, "namespace", machineKey.Namespace)
		return nil, err
	}

	if machine.Status.NodeRef == nil {
		err := errors.New("nodeRef is nil")
		r.logger.Error(err, "failed to retrieve node from the unhealthy machine")
		return nil, err
	}

	node := &v1.Node{}
	key := client.ObjectKey{
		Name:      machine.Status.NodeRef.Name,
		Namespace: machine.Status.NodeRef.Namespace,
	}

	if err := r.Get(context.Background(), key, node); err != nil {
		r.logger.Error(err, "failed to retrieve node from the unhealthy machine",
			"node name", node.Name, "machine name", machine.Name)
		return nil, err
	}

	return node, nil
}

// the unhealthy node might reboot itself and take new workloads
// since we're going to delete the node eventually, we must make sure the node is deleted only
// when there's no running workload there. Hence we mark it as unschedulable.
// markNodeAsUnschedulable sets node.Spec.Unschedulable which triggers node controller to add the taint
func (r *SelfNodeRemediationReconciler) markNodeAsUnschedulable(node *v1.Node) (ctrl.Result, error) {
	if node.Spec.Unschedulable {
		r.logger.Info("waiting for unschedulable taint to appear", "node name", node.Name)
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	node.Spec.Unschedulable = true
	r.logger.Info("Marking node as unschedulable", "node name", node.Name)
	if err := r.Client.Update(context.Background(), node); err != nil {
		if apiErrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		r.logger.Error(err, "failed to mark node as unschedulable")
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
}

func (r *SelfNodeRemediationReconciler) restoreNode(nodeToRestore *v1.Node) (ctrl.Result, error) {
	r.logger.Info("restoring node", "node name", nodeToRestore.Name)

	// todo we probably want to have some allowlist/denylist on which things to restore, we already had
	// a problem when we restored ovn annotations
	nodeToRestore.ResourceVersion = "" //create won't work with a non-empty value here
	taints, _ := utils.DeleteTaint(nodeToRestore.Spec.Taints, NodeUnschedulableTaint)
	nodeToRestore.Spec.Taints = taints
	nodeToRestore.Spec.Unschedulable = false
	nodeToRestore.CreationTimestamp = metav1.Now()
	nodeToRestore.Status = v1.NodeStatus{}

	if err := r.Client.Create(context.TODO(), nodeToRestore); err != nil {
		if apiErrors.IsAlreadyExists(err) {
			// node is already created, either by another agent or by the cluster
			r.logger.Info("failed to create node since it already exist", "node name", nodeToRestore.Name)
			return ctrl.Result{}, nil
		}
		r.logger.Error(err, "failed to create node", "node name", nodeToRestore.Name)
		return ctrl.Result{}, err
	}

	r.logger.Info("node restored successfully", "node name", nodeToRestore.Name)
	// all done, stop reconciling
	return ctrl.Result{Requeue: true}, nil
}

func (r *SelfNodeRemediationReconciler) updateSnrStatusLastError(snr *v1alpha1.SelfNodeRemediation, err error) error {
	var lastErrorVal string
	patch := client.MergeFrom(snr.DeepCopy())

	if err != nil {
		lastErrorVal = err.Error()
	} else {
		lastErrorVal = ""
	}

	if snr.Status.LastError != lastErrorVal {
		snr.Status.LastError = lastErrorVal
		updateErr := r.Client.Status().Patch(context.Background(), snr, patch)
		if updateErr != nil {
			r.logger.Error(updateErr, "Failed to update SelfNodeRemediation status")
			return updateErr
		}
	}

	_, isUnreconcilableError := err.(*UnreconcilableError)

	if isUnreconcilableError {
		// return nil to not enter reconcile again
		return nil
	}

	return err
}

func (r *SelfNodeRemediationReconciler) addNoExecuteTaint(node *v1.Node) error {
	if utils.TaintExists(node.Spec.Taints, NodeNoExecuteTaint) {
		return nil
	}

	patch := client.MergeFrom(node.DeepCopy())
	taint := *NodeNoExecuteTaint
	now := metav1.Now()
	taint.TimeAdded = &now
	node.Spec.Taints = append(node.Spec.Taints, taint)
	if err := r.Client.Patch(context.Background(), node, patch); err != nil {
		r.logger.Error(err, "Failed to add taint on node", "node name", node.Name, "taint key", NodeNoExecuteTaint.Key, "taint effect", NodeNoExecuteTaint.Effect)
		return err
	}
	r.logger.Info("NoExecute taint added", "new taints", node.Spec.Taints)
	return nil
}

func (r *SelfNodeRemediationReconciler) removeNoExecuteTaint(node *v1.Node) error {
	if !utils.TaintExists(node.Spec.Taints, NodeNoExecuteTaint) {
		return nil
	}

	patch := client.MergeFrom(node.DeepCopy())
	if taints, deleted := utils.DeleteTaint(node.Spec.Taints, NodeNoExecuteTaint); !deleted {
		r.logger.Info("Failed to remove taint from node, taint not found", "node name", node.Name, "taint key", NodeNoExecuteTaint.Key, "taint effect", NodeNoExecuteTaint.Effect)
		return nil
	} else {
		node.Spec.Taints = taints
	}

	if err := r.Client.Patch(context.Background(), node, patch); err != nil {
		r.logger.Error(err, "Failed to remove taint from node,", "node name", node.Name, "taint key", NodeNoExecuteTaint.Key, "taint effect", NodeNoExecuteTaint.Effect)
		return err
	}
	r.logger.Info("NoExecute taint removed", "new taints", node.Spec.Taints)
	return nil
}

func (r *SelfNodeRemediationReconciler) isStoppedByNHC(snr *v1alpha1.SelfNodeRemediation) bool {
	if snr != nil && snr.Annotations != nil {
		_, isTimeoutIssued := snr.Annotations[nhcTimeOutAnnotation]
		return isTimeoutIssued
	}
	return false
}

func (r *SelfNodeRemediationReconciler) addOutOfServiceTaint(node *v1.Node) error {
	if utils.TaintExists(node.Spec.Taints, OutOfServiceTaint) {
		return nil
	}

	patch := client.MergeFrom(node.DeepCopy())
	taint := *OutOfServiceTaint
	now := metav1.Now()
	taint.TimeAdded = &now
	node.Spec.Taints = append(node.Spec.Taints, taint)
	if err := r.Client.Patch(context.Background(), node, patch); err != nil {
		r.logger.Error(err, "Failed to add out-of-service taint on node", "node name", node.Name)
		return err
	}
	r.logger.Info("outofservice taint added", "new taints", node.Spec.Taints)
	return nil
}

func (r *SelfNodeRemediationReconciler) removeOutOfServiceTaint(node *v1.Node) error {

	if !utils.TaintExists(node.Spec.Taints, OutOfServiceTaint) {
		return nil
	}

	patch := client.MergeFrom(node.DeepCopy())
	if taints, deleted := utils.DeleteTaint(node.Spec.Taints, OutOfServiceTaint); !deleted {
		r.logger.Info("Failed to remove taint from node, taint not found", "node name", node.Name, "taint key", OutOfServiceTaint.Key, "taint effect", OutOfServiceTaint.Effect)
		return nil
	} else {
		node.Spec.Taints = taints
	}
	if err := r.Client.Patch(context.Background(), node, patch); err != nil {
		r.logger.Error(err, "Failed to remove taint from node,", "node name", node.Name, "taint key", OutOfServiceTaint.Key, "taint effect", OutOfServiceTaint.Effect)
		return err
	}
	return nil
}

func (r *SelfNodeRemediationReconciler) isResourceDeletionCompleted(node *v1.Node) bool {
	pods := &v1.PodList{}
	if err := r.Client.List(context.Background(), pods); err != nil {
		r.logger.Error(err, "failed to get pod list")
		return false
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == node.Name && r.isPodTerminating(&pod) {
			return false
		}
	}
	volumeAttachments := &storagev1.VolumeAttachmentList{}
	if err := r.Client.List(context.Background(), volumeAttachments); err != nil {
		r.logger.Error(err, "failed to get volumeAttachments list")
		return false
	}
	for _, va := range volumeAttachments.Items {
		if va.Spec.NodeName == node.Name {
			return false
		}
	}

	return true
}

func (r *SelfNodeRemediationReconciler) isPodTerminating(pod *v1.Pod) bool {
	return pod.ObjectMeta.DeletionTimestamp != nil
}

func (r *SelfNodeRemediationReconciler) isResourceDeletionExpired(snr *v1alpha1.SelfNodeRemediation) (bool, time.Duration) {
	waitTime := snr.Status.TimeAssumedRebooted.Add(300 * time.Second)

	if waitTime.After(time.Now()) {
		return false, 5 * time.Second
	}

	return true, 0
}
