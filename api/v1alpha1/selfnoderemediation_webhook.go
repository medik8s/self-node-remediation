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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	webhookRemediationLog = logf.Log.WithName("selfnoderemediation-resource")
)

func (r *SelfNodeRemediation) SetupWebhookWithManager(mgr ctrl.Manager) error {

	if err := initOutOfServiceTaintSupportedFlag(mgr.GetConfig()); err != nil {
		return err
	}

	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediation,mutating=false,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediations,verbs=create;update,versions=v1alpha1,name=vselfnoderemediation.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &SelfNodeRemediation{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediation) ValidateCreate() error {
	webhookRemediationLog.Info("validate create", "name", r.Name)
	return validateStrategy(r.Spec)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediation) ValidateUpdate(_ runtime.Object) error {
	webhookRemediationLog.Info("validate update", "name", r.Name)
	return validateStrategy(r.Spec)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediation) ValidateDelete() error {
	// unused for now, add "delete" when needed to verbs in the kubebuilder annotation above
	webhookRemediationLog.Info("validate delete", "name", r.Name)
	return nil
}
