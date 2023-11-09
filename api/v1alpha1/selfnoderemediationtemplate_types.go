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
	"github.com/medik8s/common/pkg/labels"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultTemplateName = "self-node-remediation-resource-deletion-template"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type SelfNodeRemediationTemplateResource struct {
	Spec SelfNodeRemediationSpec `json:"spec"`
}

// SelfNodeRemediationTemplateSpec defines the desired state of SelfNodeRemediationTemplate
type SelfNodeRemediationTemplateSpec struct {
	// Template defines the desired state of SelfNodeRemediationTemplate
	//+operator-sdk:csv:customresourcedefinitions:type=spec
	Template SelfNodeRemediationTemplateResource `json:"template"`
}

// SelfNodeRemediationTemplateStatus defines the observed state of SelfNodeRemediationTemplate
type SelfNodeRemediationTemplateStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=snrt;snremediationtemplate;snrtemplate

// SelfNodeRemediationTemplate is the Schema for the selfnoderemediationtemplates API
// +operator-sdk:csv:customresourcedefinitions:resources={{"SelfNodeRemediationTemplate","v1alpha1","selfnoderemediationtemplates"}}
type SelfNodeRemediationTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SelfNodeRemediationTemplateSpec   `json:"spec,omitempty"`
	Status SelfNodeRemediationTemplateStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SelfNodeRemediationTemplateList contains a list of SelfNodeRemediationTemplate
type SelfNodeRemediationTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SelfNodeRemediationTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SelfNodeRemediationTemplate{}, &SelfNodeRemediationTemplateList{})
}

func NewRemediationTemplates() []*SelfNodeRemediationTemplate {
	templateLabels := make(map[string]string)
	templateLabels[labels.DefaultTemplate] = "true"
	return []*SelfNodeRemediationTemplate{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   defaultTemplateName,
				Labels: templateLabels,
			},
			Spec: SelfNodeRemediationTemplateSpec{
				Template: SelfNodeRemediationTemplateResource{
					Spec: SelfNodeRemediationSpec{
						RemediationStrategy: DefaultRemediationStrategy,
					},
				},
			},
		},
	}
}
