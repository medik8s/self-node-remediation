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
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"time"
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

//+kubebuilder:webhook:path=/validate-self-node-remediation-medik8s-io-v1alpha1-selfnoderemediationconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=self-node-remediation.medik8s.io,resources=selfnoderemediationconfigs,verbs=create;update,versions=v1alpha1,name=vselfnoderemediationconfig.kb.io,admissionReviewVersions={v1,v1beta1}

var _ webhook.Validator = &SelfNodeRemediationConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationConfig) ValidateCreate() error {
	selfNodeRemediationConfigLog.Info("validate create", "name", r.Name)

	return r.validateTimes()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationConfig) ValidateUpdate(old runtime.Object) error {
	selfNodeRemediationConfigLog.Info("validate update", "name", r.Name)

	return r.validateTimes()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *SelfNodeRemediationConfig) ValidateDelete() error {
	selfNodeRemediationConfigLog.Info("validate delete", "name", r.Name)

	return nil
}

// validateTimes validates that each time field in the SelfNodeRemediationConfig CR doesn't go below the minimum time
// that was defined to it
func (r *SelfNodeRemediationConfig) validateTimes() error {

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
			return err
		}
	}

	return nil
}

func (f *field) validate() error {
	if f.durationValue < f.minDurationValue {
		err := fmt.Errorf(f.name + " cannot be less than " + f.minDurationValue.String())
		selfNodeRemediationConfigLog.Error(err, "invalid duration value for field " + f.name)
		return err
	}

	return nil
}
