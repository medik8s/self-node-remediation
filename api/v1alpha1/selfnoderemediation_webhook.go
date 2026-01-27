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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	webhookRemediationLog = logf.Log.WithName("selfnoderemediation-resource")
)

func (r *SelfNodeRemediation) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		WithValidator(&SNRValidator{}).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediation,mutating=false,failurePolicy=ignore,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediations,verbs=create;update,versions=v1alpha1,name=vselfnoderemediation.kb.io,admissionReviewVersions=v1

type SNRValidator struct{}

var _ admission.CustomValidator = &SNRValidator{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (v *SNRValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	snr, ok := obj.(*SelfNodeRemediation)
	if !ok {
		return nil, fmt.Errorf("expected a SelfNodeRemediation but got a %T", obj)
	}
	webhookRemediationLog.Info("validate create", "name", snr.Name)
	return admission.Warnings{}, validateStrategy(snr.Spec)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (v *SNRValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	snr, ok := newObj.(*SelfNodeRemediation)
	if !ok {
		return nil, fmt.Errorf("expected a SelfNodeRemediation but got a %T", newObj)
	}
	webhookRemediationLog.Info("validate update", "name", snr.Name)
	return admission.Warnings{}, validateStrategy(snr.Spec)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (v *SNRValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	snr, ok := obj.(*SelfNodeRemediation)
	if !ok {
		return nil, fmt.Errorf("expected a SelfNodeRemediation but got a %T", obj)
	} // unused for now, add "delete" when needed to verbs in the kubebuilder annotation above
	webhookRemediationLog.Info("validate delete", "name", snr.Name)
	return admission.Warnings{}, nil
}
