// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: Run "make" to regenerate code after modifying this file.

// Korrel8rSpec defines the desired state of Korrel8r
type Korrel8rSpec struct {
	// ServiceAccountName for the korrel8r deployment, use 'default' if missing.
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// ConfigMap containing optional custom configuration for Korrel8r.
	//
	// If specified, the map must contain the key 'korrel8r.yaml'.
	// This will be used as the primary configuration file.
	//
	// The entire ConfigMap is mounted in the same directory,
	// so korrel8r.yaml can include other files from the same ConfigMap.
	//
	// The default korrel8r configuration files can also be included,
	// they are available under /etc/korrel8r in the image.
	//
	// See https://korrel8r.github.io/korrel8r/#configuration for more about configuring Korrel8r.
	//
	ConfigMap *corev1.LocalObjectReference `json:"configMap,omitempty"`

	// Debug provides optional settings intended to help with debugging problems.
	Debug *DebugSpec `json:"debug,omitempty"`
}

type DebugSpec struct {
	// Verbose sets the numeric logging verbosity for the KORREL8R_VERBOSE environment variable.
	Verbose int `json:"verbose,omitempty"`
}

// Korrel8rStatus defines the observed state of Korrel8r
type Korrel8rStatus struct {
	// Conditions conditions.type is one of "Available", "Progressing", and "Degraded"
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Korrel8r is a service that correlates observabililty signals in the cluster.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
