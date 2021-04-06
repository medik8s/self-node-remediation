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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PoisonPillRemediationSpec defines the desired state of PoisonPillRemediation
type PoisonPillRemediationSpec struct {
	// Important: Run "make" to regenerate code after modifying this file
}

// PoisonPillRemediationStatus defines the observed state of PoisonPillRemediation
type PoisonPillRemediationStatus struct {
	// Important: Run "make" to regenerate code after modifying this file

	//NodeBackup is the node object that is going to be deleted as part of the remediation process
	// +optional
	// +kubebuilder:validation:EmbeddedResource
	// +kubebuilder:pruning:PreserveUnknownFields
	NodeBackup *v1.Node `json:"nodeBackup,omitempty"`

	//TimeAssumedRebooted is the time by then the unhealthy node assumed to be rebooted
	// +optional
	TimeAssumedRebooted *metav1.Time `json:"timeAssumedRebooted,omitempty"`

	// Phase represents the current phase of remediation,
	// One of: TBD
	// +optional
	Phase *string `json:"phase,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// PoisonPillRemediation is the Schema for the poisonpillremediations API
type PoisonPillRemediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PoisonPillRemediationSpec   `json:"spec,omitempty"`
	Status PoisonPillRemediationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PoisonPillRemediationList contains a list of PoisonPillRemediation
type PoisonPillRemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PoisonPillRemediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PoisonPillRemediation{}, &PoisonPillRemediationList{})
}
