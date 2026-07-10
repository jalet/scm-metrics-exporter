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

	// RateLimitThreshold pauses new collection Jobs when the shared credential's remaining
	// API budget falls below this floor, resuming after the provider's rate-limit window
	// resets. It complements Parallelism: Parallelism bounds concurrent pressure, while this
	// stops dispatch entirely once the budget runs low. 0 disables the guard.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=200
	// +optional
	RateLimitThreshold int32 `json:"rateLimitThreshold,omitempty"`

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

	// FindingDimensions adds optional labels: "ecosystem" (Dependabot package ecosystem) and
	// "tool" (scanning tool) on the security-findings metric, and "severity" on the
	// remediation histogram. Off by default because each multiplies series cardinality.
	// +kubebuilder:validation:items:Enum=ecosystem;tool;severity
	// +optional
	FindingDimensions []string `json:"findingDimensions,omitempty"`

	// CollectWorkflows enables recent CI-run metrics (scm_workflow_runs_recent): GitHub
	// Actions workflow runs, or GitLab pipelines. Off by default because it adds an API
	// call per repository and extra series cardinality.
	// +optional
	CollectWorkflows bool `json:"collectWorkflows,omitempty"`

	// WorkflowLookback bounds how far back CI runs are counted (used when CollectWorkflows
	// is true).
	// +kubebuilder:default="168h"
	// +optional
	WorkflowLookback metav1.Duration `json:"workflowLookback,omitempty"`

	// CredentialsSecret names the Secret in the CR's namespace holding the provider
	// credentials referenced by the provider-specific key fields.
	CredentialsSecret corev1.LocalObjectReference `json:"credentialsSecret"`

	// CollectLifecycle enables resolved-alert collection: the remediation-time histogram
	// (scm_finding_remediation_seconds) and the finding-state gauge (scm_findings_by_state).
	// Requires Valkey. Off by default.
	// +kubebuilder:default=false
	// +optional
	CollectLifecycle bool `json:"collectLifecycle,omitempty"`

	// ResolutionWindow bounds how far back resolved alerts are collected and is the
	// deduplication TTL. Used when CollectLifecycle is true.
	// +kubebuilder:default="2160h"
	// +optional
	ResolutionWindow metav1.Duration `json:"resolutionWindow,omitempty"`

	// Valkey configures the store backing the remediation histogram. Required when
	// CollectLifecycle is true.
	// +optional
	Valkey *ValkeyConfig `json:"valkey,omitempty"`
}

// ValkeyConfig points the collection Jobs at a Valkey endpoint for lifecycle dedup state.
type ValkeyConfig struct {
	// Endpoint is the Valkey host:port (for example "valkey.observability:6379").
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// SecretRef names a Secret in the CR namespace holding the Valkey password under
	// PasswordKey. Omit for a passwordless Valkey.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// PasswordKey is the Secret key holding the Valkey password (default "password").
	// +kubebuilder:default="password"
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
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

	// Exclude removes repositories from the Include set. An empty Exclude removes nothing;
	// a repository is dropped when it matches every set criterion of Exclude (ANDed).
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
