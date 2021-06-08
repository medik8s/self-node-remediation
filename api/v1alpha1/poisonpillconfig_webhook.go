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
var poisonpillconfiglog = logf.Log.WithName("poisonpillconfig-webhook")

// SetupWebhookWithManager sets a webhook with the passed manager
func (r *PoisonPillConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&PoisonPillConfig{}).
		Complete()
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-poison-pill-medik8s-io-v1alpha1-poisonpillconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=poison-pill.medik8s.io,resources=poisonpillconfigs,verbs=create;update,versions=v1alpha1,name=vpoisonpillconfig.kb.io,admissionReviewVersions={v1,v1beta1}

var _ webhook.Validator = &PoisonPillConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *PoisonPillConfig) ValidateCreate() error {
	poisonpillconfiglog.Info("validate create", "name", r.Name)
	if r.Name != configCRName {
		return fmt.Errorf("the name of the PoisonPillConfig object must be %s but found %s", configCRName, r.Name)
	}
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *PoisonPillConfig) ValidateUpdate(old runtime.Object) error {
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *PoisonPillConfig) ValidateDelete() error {
	return nil
}
