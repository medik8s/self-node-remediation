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
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	//out of service taint strategy const (supported from 1.26)
	minK8sMajorVersionSupportingOutOfServiceTaint = 1
	minK8sMinorVersionSupportingOutOfServiceTaint = 26
)

var (
	// snrtWebookLog is for logging in this package.
	snrtWebookLog = logf.Log.WithName("selfnoderemediationtemplate-resource")
	//out of service taint strategy params
	isOutOfServiceTaintSupported bool
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
	snrtWebookLog.Info("validate create", "name", r.Name)
	return validateStrategy(r.Spec.Template.Spec)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateUpdate(_ runtime.Object) error {
	snrtWebookLog.Info("validate update", "name", r.Name)
	return validateStrategy(r.Spec.Template.Spec)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationTemplate) ValidateDelete() error {
	// unused for now, add "delete" when needed to verbs in the kubebuilder annotation above
	snrtWebookLog.Info("validate delete", "name", r.Name)
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
		snrtWebookLog.Error(err, "couldn't get retrieve k8s client")
		return err
	} else if version, err := cs.Discovery().ServerVersion(); err != nil || version == nil {
		if version == nil {
			err = fmt.Errorf("k8s server version is nil")
		}
		snrtWebookLog.Error(err, "couldn't get retrieve k8s server version")
		return err
	} else {
		isOutOfServiceTaintSupported = strings.Compare(fmt.Sprintf("%s.%s", version.Major, version.Minor), "1.26") >= 0

		var majorVer, minorVer int64
		if majorVer, err = strconv.ParseInt(version.Major, 10, 8); err != nil {
			snrtWebookLog.Error(err, "couldn't parse k8s major version", "major version", version.Major)
			return err
		}

		if minorVer, err = strconv.ParseInt(version.Major, 10, 8); err != nil {
			snrtWebookLog.Error(err, "couldn't parse k8s minor version", "minor version", version.Minor)
			return err
		}

		isOutOfServiceTaintSupported = majorVer > minK8sMajorVersionSupportingOutOfServiceTaint || (majorVer == minK8sMajorVersionSupportingOutOfServiceTaint && minorVer >= minK8sMinorVersionSupportingOutOfServiceTaint)
		snrtWebookLog.Info("out of service taint strategy", "isSupported", isOutOfServiceTaintSupported, "k8sMajorVersion", majorVer, "k8sMinorVersion", minorVer)
		return nil
	}
}
