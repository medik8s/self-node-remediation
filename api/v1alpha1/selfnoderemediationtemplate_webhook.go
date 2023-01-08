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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var selfnoderemediationtemplatelog = logf.Log.WithName("selfnoderemediationtemplate-resource")

func (r *SelfNodeRemediationTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=create;update,versions=v1alpha1,name=vselfnoderemediationtemplate.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &SelfNodeRemediationTemplate{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateCreate() error {
	selfnoderemediationtemplatelog.Info("validate create", "name", r.Name)
	return ValidateStrategy(r.Spec.Template.Spec)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateUpdate(old runtime.Object) error {
	selfnoderemediationtemplatelog.Info("validate update", "name", r.Name)
	return ValidateStrategy(r.Spec.Template.Spec)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateDelete() error {
	// unused for now, add "delete" when needed to verbs in the kubebuilder annotation above
	selfnoderemediationtemplatelog.Info("validate delete", "name", r.Name)
	return nil
}

func ValidateStrategy(snrSpec SelfNodeRemediationSpec) error {
	if snrSpec.RemediationStrategy == DeprecatedNodeDeletionRemediationStrategy {
		return fmt.Errorf("%s is deprecated, please switch to %s", DeprecatedNodeDeletionRemediationStrategy, ResourceDeletionRemediationStrategy)
	} else if snrSpec.RemediationStrategy != ResourceDeletionRemediationStrategy {
		return fmt.Errorf("invalid remediation strategy %s", snrSpec.RemediationStrategy)
	}
	return nil
}
