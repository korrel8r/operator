/*
Copyright 2023 Alan Conway.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Korrel8rSpec defines the desired state of Korrel8r
type Korrel8rSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of Korrel8r. Edit korrel8r_types.go to remove/update
	Foo string `json:"foo,omitempty"`
}

// Korrel8rStatus defines the observed state of Korrel8r
type Korrel8rStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Korrel8r is the Schema for the korrel8rs API
type Korrel8r struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   Korrel8rSpec   `json:"spec,omitempty"`
	Status Korrel8rStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// Korrel8rList contains a list of Korrel8r
type Korrel8rList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Korrel8r `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Korrel8r{}, &Korrel8rList{})
}
