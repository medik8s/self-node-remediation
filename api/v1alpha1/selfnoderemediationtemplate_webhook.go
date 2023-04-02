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
	"strconv"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var (
	log = logf.Log.WithName("selfnoderemediationtemplate-resource")

	//out of service taint strategy params
	isOutOfServiceTaintSupported             bool
	minK8sVersionSupportingOutOfServiceTaint = 1.26
)

func (r *SelfNodeRemediationTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {

	if err := initOutOfServiceTaintSupportedFlag(mgr.GetConfig()); err != nil {
		return err
	}

	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationtemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=create;update,versions=v1alpha1,name=vselfnoderemediationtemplate.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &SelfNodeRemediationTemplate{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateCreate() error {
	log.Info("validate create", "name", r.Name)
	return validateStrategy(r.Spec.Template.Spec)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateUpdate(_ runtime.Object) error {
	log.Info("validate update", "name", r.Name)
	return validateStrategy(r.Spec.Template.Spec)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateDelete() error {
	// unused for now, add "delete" when needed to verbs in the kubebuilder annotation above
	log.Info("validate delete", "name", r.Name)
	return nil
}

func validateStrategy(snrSpec SelfNodeRemediationSpec) error {
	if snrSpec.RemediationStrategy == OutOfServiceTaintRemediationStrategy && !isOutOfServiceTaintSupported {
		return fmt.Errorf("%s remediation strategy is not supported at kubernetes version lower than 1.26, please use a different remediation strategy", OutOfServiceTaintRemediationStrategy)
	}
	return nil
}

func initOutOfServiceTaintSupportedFlag(config *rest.Config) error {
	if cs, err := kubernetes.NewForConfig(config); err != nil || cs == nil {
		if cs == nil {
			err = fmt.Errorf("k8s client set is nil")
		}
		log.Error(err, "couldn't get retrieve k8s client")
		return err
	} else if version, err := cs.Discovery().ServerVersion(); err != nil || version == nil {
		if version == nil {
			err = fmt.Errorf("k8s server version is nil")
		}
		log.Error(err, "couldn't get retrieve k8s server version")
		return err
	} else {
		k8sVersion, err := strconv.ParseFloat(fmt.Sprintf("%s.%s", version.Major, version.Minor), 8)
		if err != nil {
			log.Error(err, "couldn't get parse k8s server version", "Major", version.Major, "Minor", version.Minor)
			return err
		}

		isOutOfServiceTaintSupported = k8sVersion >= minK8sVersionSupportingOutOfServiceTaint
		log.Info("out of service taint strategy", "isSupported", isOutOfServiceTaintSupported, "k8sVersion", k8sVersion)
		return nil
	}
}
