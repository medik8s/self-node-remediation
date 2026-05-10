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

	commonAnnotations "github.com/medik8s/common/pkg/annotations"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	remediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/pkg/utils"
)

var (
	// webhookTemplateLog is for logging in this package.
	webhookTemplateLog = logf.Log.WithName("selfnoderemediationtemplate-resource")
)

func SetupSNRTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&remediationv1alpha1.SelfNodeRemediationTemplate{}).
		WithDefaulter(&SNRTemplateDefaulter{}).
		WithValidator(&SNRTemplateValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=create;update,versions=v1alpha1,name=mselfnoderemediationtemplate.kb.io,admissionReviewVersions=v1

type SNRTemplateDefaulter struct{}

var _ admission.CustomDefaulter = &SNRTemplateDefaulter{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (d *SNRTemplateDefaulter) Default(_ context.Context, obj runtime.Object) error {
	snrTemplate, ok := obj.(*remediationv1alpha1.SelfNodeRemediationTemplate)
	if !ok {
		return fmt.Errorf("expected a SelfNodeRemediationTemplate but got a %T", obj)
	}
	webhookTemplateLog.Info("default", "name", snrTemplate.Name)
	if snrTemplate.GetAnnotations() == nil {
		snrTemplate.Annotations = make(map[string]string)
	}
	if _, isSameKindSupported := snrTemplate.GetAnnotations()[commonAnnotations.MultipleTemplatesSupportedAnnotation]; !isSameKindSupported {
		snrTemplate.Annotations[commonAnnotations.MultipleTemplatesSupportedAnnotation] = "true"
	}
	return nil
}

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=create;update,versions=v1alpha1,name=vselfnoderemediationtemplate.kb.io,admissionReviewVersions=v1

type SNRTemplateValidator struct{}

var _ admission.CustomValidator = &SNRTemplateValidator{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (v *SNRTemplateValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	snrTemplate, ok := obj.(*remediationv1alpha1.SelfNodeRemediationTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a SelfNodeRemediationTemplate but got a %T", obj)
	}
	webhookTemplateLog.Info("validate create", "name", snrTemplate.Name)
	return admission.Warnings{}, validateStrategy(snrTemplate.Spec.Template.Spec)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (v *SNRTemplateValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	snrTemplate, ok := newObj.(*remediationv1alpha1.SelfNodeRemediationTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a SelfNodeRemediationTemplate but got a %T", newObj)
	}
	webhookTemplateLog.Info("validate update", "name", snrTemplate.Name)
	return admission.Warnings{}, validateStrategy(snrTemplate.Spec.Template.Spec)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (v *SNRTemplateValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	snrTemplate, ok := obj.(*remediationv1alpha1.SelfNodeRemediationTemplate)
	if !ok {
		return nil, fmt.Errorf("expected a SelfNodeRemediationTemplate but got a %T", obj)
	}
	// unused for now, add "delete" when needed to verbs in the kubebuilder annotation above
	webhookTemplateLog.Info("validate delete", "name", snrTemplate.Name)
	return admission.Warnings{}, nil
}

func validateStrategy(snrSpec remediationv1alpha1.SelfNodeRemediationSpec) error {
	if snrSpec.RemediationStrategy == remediationv1alpha1.OutOfServiceTaintRemediationStrategy && !utils.IsOutOfServiceTaintSupported {
		return fmt.Errorf("%s remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy", remediationv1alpha1.OutOfServiceTaintRemediationStrategy)
	}
	return nil
}
