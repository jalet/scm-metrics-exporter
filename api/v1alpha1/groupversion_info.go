// Package v1alpha1 contains the API schema definitions for the scm v1alpha1 API
// group (GitHubMetricsExporter, GitLabMetricsExporter). The concrete types are
// added in Epic 08. This package depends only on apimachinery, per the
// controller-runtime guidance that API packages stay easy to import.
//
// +kubebuilder:object:generate=true
// +groupName=scm.jalet.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group/version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "scm.jalet.io", Version: "v1alpha1"}

	// SchemeBuilder collects the functions that register this group's types. Each
	// type file appends to it from its init (see Epic 08).
	SchemeBuilder = &runtime.SchemeBuilder{}
)

// AddToScheme adds this group-version's registered types to a scheme. It resolves
// SchemeBuilder at call time, so type registrations added in init blocks are
// included.
func AddToScheme(s *runtime.Scheme) error {
	return SchemeBuilder.AddToScheme(s)
}
