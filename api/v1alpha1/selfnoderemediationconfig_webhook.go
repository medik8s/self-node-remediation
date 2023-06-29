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
	"os"
	"path/filepath"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	webhookCertDir  = "/apiserver.local.config/certificates"
	webhookCertName = "apiserver.crt"
	webhookKeyName  = "apiserver.key"
)

// fields names
const (
	peerApiServerTimeout = "PeerApiServerTimeout"
	apiServerTimeout     = "ApiServerTimeout"
	peerDialTimeout      = "PeerDialTimeout"
	peerRequestTimeout   = "PeerRequestTimeout"
	apiCheckInterval     = "ApiCheckInterval"
	peerUpdateInterval   = "PeerUpdateInterval"
)

// minimal time durations allowed for fields
const (
	minDurPeerApiServerTimeout = 10 * time.Millisecond
	minDurApiServerTimeout     = 10 * time.Millisecond
	minDurPeerDialTimeout      = 10 * time.Millisecond
	minDurPeerRequestTimeout   = 10 * time.Millisecond
	minDurApiCheckInterval     = 1 * time.Second
	minDurPeerUpdateInterval   = 10 * time.Second
)

type field struct {
	name             string
	durationValue    time.Duration
	minDurationValue time.Duration
}

// log is for logging in this package.
var selfNodeRemediationConfigLog = logf.Log.WithName("selfnoderemediationconfig-resource")

func (r *SelfNodeRemediationConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {

	// check if OLM injected certs
	certs := []string{filepath.Join(webhookCertDir, webhookCertName), filepath.Join(webhookCertDir, webhookKeyName)}
	certsInjected := true
	for _, fname := range certs {
		if _, err := os.Stat(fname); err != nil {
			certsInjected = false
			break
		}
	}
	if certsInjected {
		server := mgr.GetWebhookServer()
		server.CertDir = webhookCertDir
		server.CertName = webhookCertName
		server.KeyName = webhookKeyName
	} else {
		selfNodeRemediationConfigLog.Info("OLM injected certs for webhooks not found")
	}

	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediationconfigs,verbs=create;update,versions=v1alpha1,name=vselfnoderemediationconfig.kb.io,admissionReviewVersions={v1}

var _ webhook.Validator = &SelfNodeRemediationConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationConfig) ValidateCreate() error {
	selfNodeRemediationConfigLog.Info("validate create", "name", r.Name)

	return errors.NewAggregate([]error{
		r.validateTimes(),
		r.validateCustomTolerations(),
	})

}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationConfig) ValidateUpdate(_ runtime.Object) error {
	selfNodeRemediationConfigLog.Info("validate update", "name", r.Name)

	return errors.NewAggregate([]error{
		r.validateTimes(),
		r.validateCustomTolerations(),
	})
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationConfig) ValidateDelete() error {
	selfNodeRemediationConfigLog.Info("validate delete", "name", r.Name)

	return nil
}

// validateTimes validates that each time field in the SelfNodeRemediationConfig CR doesn't go below the minimum time
// that was defined to it
func (r *SelfNodeRemediationConfig) validateTimes() error {
	errMsg := ""

	s := r.Spec
	fields := []field{
		{peerApiServerTimeout, s.PeerApiServerTimeout.Duration, minDurPeerApiServerTimeout},
		{apiServerTimeout, s.ApiServerTimeout.Duration, minDurApiServerTimeout},
		{peerDialTimeout, s.PeerDialTimeout.Duration, minDurPeerDialTimeout},
		{peerRequestTimeout, s.PeerRequestTimeout.Duration, minDurPeerRequestTimeout},
		{apiCheckInterval, s.ApiCheckInterval.Duration, minDurApiCheckInterval},
		{peerUpdateInterval, s.PeerUpdateInterval.Duration, minDurPeerUpdateInterval},
	}

	for _, field := range fields {
		err := field.validate()
		if err != nil {
			errMsg += "\n" + err.Error()
		}
	}

	if errMsg != "" {
		return fmt.Errorf(errMsg)
	}
	return nil
}

func (f *field) validate() error {
	if f.durationValue < f.minDurationValue {
		err := fmt.Errorf(f.name + " cannot be less than " + f.minDurationValue.String())
		return err
	}

	return nil
}

func (r *SelfNodeRemediationConfig) validateCustomTolerations() error {
	customTolerations := r.Spec.CustomDsTolerations
	for _, toleration := range customTolerations {
		if err := validateToleration(toleration); err != nil {
			return err
		}
	}
	return nil
}

func validateToleration(toleration v1.Toleration) error {
	if len(toleration.Operator) > 0 {
		switch toleration.Operator {
		case v1.TolerationOpEqual:
		//Valid nothing to do
		case v1.TolerationOpExists:
			if len(toleration.Value) != 0 {
				err := fmt.Errorf("invalid value for tolerarion, value must be empty for Operator value is Exists")
				selfNodeRemediationConfigLog.Error(err, "invalid value for tolerarion, value must be empty for Operator value is Exists")
				return err
			}
		default:
			err := fmt.Errorf("invalid operator for tolerarion: %s", toleration.Operator)
			selfNodeRemediationConfigLog.Error(err, "invalid operator for tolerarion", "valid values", []v1.TolerationOperator{v1.TolerationOpEqual, v1.TolerationOpExists}, "received value", toleration.Operator)
			return err
		}
	}

	if len(toleration.Effect) > 0 {
		switch toleration.Effect {
		case v1.TaintEffectNoSchedule, v1.TaintEffectPreferNoSchedule, v1.TaintEffectNoExecute:
			//Valid nothing to do
		default:
			err := fmt.Errorf("invalid taint effect for tolerarion: %s", toleration.Effect)
			selfNodeRemediationConfigLog.Error(err, "invalid taint effect for tolerarion", "valid values", []v1.TaintEffect{v1.TaintEffectNoSchedule, v1.TaintEffectPreferNoSchedule, v1.TaintEffectNoExecute}, "received value", toleration.Effect)
			return err
		}
	}
	return nil
}
