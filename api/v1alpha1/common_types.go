package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExporterSpec is the configuration shared by every provider CRD. Provider specs
// embed it inline and add their provider-specific fields.
//
// The operator discovers the target's repositories on DiscoveryInterval and dispatches
// one ephemeral collection Job per repository, capped by Parallelism. Each Job collects a
// single repository and pushes over OTLP, so metrics are push-based (no Service to scrape).
type ExporterSpec struct {
	// DiscoveryInterval is how often the operator re-discovers repositories and
	// re-dispatches collection Jobs.
	// +kubebuilder:default="15m"
	// +optional
	DiscoveryInterval metav1.Duration `json:"discoveryInterval,omitempty"`

	// Parallelism caps the number of concurrent per-repo collection Jobs. It is the
	// rate-limit governor: it bounds concurrent API pressure on the shared credential and
	// keeps request bursts under the provider's secondary limits.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	// +optional
	Parallelism int32 `json:"parallelism,omitempty"`

	// Image overrides the exporter container image run by the collection Jobs. When empty
	// the operator injects its own image reference.
	// +optional
	Image string `json:"image,omitempty"`

	// Resources are the collection Job container's compute resources.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Export configures how metrics leave the collection Jobs (OTLP push).
	Export ExportConfig `json:"export"`

	// AutoDiscover filters which repositories the operator collects.
	// +optional
	AutoDiscover AutoDiscover `json:"autoDiscover,omitempty"`

	// FindingDimensions adds optional labels to the security-findings metric: "ecosystem"
	// (Dependabot package ecosystem) and "tool" (scanning tool). Off by default because
	// they multiply series cardinality.
	// +kubebuilder:validation:items:Enum=ecosystem;tool
	// +optional
	FindingDimensions []string `json:"findingDimensions,omitempty"`

	// CredentialsSecret names the Secret in the CR's namespace holding the provider
	// credentials referenced by the provider-specific key fields.
	CredentialsSecret corev1.LocalObjectReference `json:"credentialsSecret"`
}

// ExportConfig controls the OpenTelemetry OTLP export used by the collection Jobs.
type ExportConfig struct {
	// OTLPEndpoint is the OTLP metrics endpoint the collection Jobs push to (for example
	// "http://otel-collector.observability:4318"). Required: ephemeral Jobs cannot be
	// scraped, so there is always a push target.
	// +kubebuilder:validation:MinLength=1
	OTLPEndpoint string `json:"otlpEndpoint"`
}

// AutoDiscover selects which repositories are collected. Include chooses the candidate
// set (empty Include matches all repositories); Exclude removes from it.
type AutoDiscover struct {
	// Include selects repositories to collect. An empty Include matches every repository
	// in the target.
	// +optional
	Include RepoFilter `json:"include,omitempty"`

	// Exclude removes repositories from the Include set. Reserved: the schema is stable but
	// exclusion is not yet enforced.
	// +optional
	Exclude RepoFilter `json:"exclude,omitempty"`
}

// RepoFilter matches repositories by attribute. Within a filter the criteria are ANDed; an
// empty filter matches everything.
type RepoFilter struct {
	// Topics matches repositories carrying any of these topics.
	// +optional
	Topics []string `json:"topics,omitempty"`

	// Visibility matches repositories with any of these visibilities.
	// +kubebuilder:validation:items:Enum=public;private;internal
	// +optional
	Visibility []string `json:"visibility,omitempty"`

	// NamePatterns matches repository names against these shell-style globs (for example
	// "service-*").
	// +optional
	NamePatterns []string `json:"namePatterns,omitempty"`

	// Archived, when set, matches only archived (true) or only non-archived (false)
	// repositories; unset matches both.
	// +optional
	Archived *bool `json:"archived,omitempty"`
}

// ExporterStatus is the shared status for provider CRDs.
type ExporterStatus struct {
	// ObservedGeneration is the .metadata.generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// DiscoveredRepositories lists the repositories found at the last successful discovery,
	// that the operator dispatches collection Jobs for.
	// +optional
	DiscoveredRepositories []string `json:"discoveredRepositories,omitempty"`

	// LastDiscoveryTime is when discovery last succeeded.
	// +optional
	LastDiscoveryTime *metav1.Time `json:"lastDiscoveryTime,omitempty"`

	// Conditions describe the current state of the exporter.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
