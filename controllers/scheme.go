package controllers

import (
	"errors"

	korrel8rv1alpha1 "github.com/korrel8r/operator/api/v1alpha1"
	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

// AddToScheme adds all types needed by controllers.
func AddToScheme(s *runtime.Scheme) error {
	return errors.Join(
		scheme.AddToScheme(s),
		korrel8rv1alpha1.AddToScheme(s),
		routev1.AddToScheme(s),
	)
	//+kubebuilder:scaffold:scheme
}
