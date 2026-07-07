package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExporterSpec is the configuration shared by every provider CRD. Provider specs
// embed it inline and add their provider-specific fields.
type ExporterSpec struct {
	// PollInterval is how often the exporter polls the provider.
	// +kubebuilder:default="5m"
	// +optional
	PollInterval metav1.Duration `json:"pollInterval,omitempty"`

	// Image overrides the exporter container image. When empty the operator injects
	// its own image reference.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is the number of exporter pods.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Resources are the exporter container's compute resources.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Export selects and tunes how metrics leave the exporter.
	// +optional
	Export ExportConfig `json:"export,omitempty"`

	// ServiceMonitor, when true, makes the operator create a Prometheus Operator
	// ServiceMonitor for the exporter (requires the ServiceMonitor CRD present).
	// +optional
	ServiceMonitor bool `json:"serviceMonitor,omitempty"`

	// CredentialsSecret names the Secret in the CR's namespace holding the provider
	// credentials referenced by the provider-specific key fields.
	CredentialsSecret corev1.LocalObjectReference `json:"credentialsSecret"`
}

// ExportConfig controls the OpenTelemetry exporter used by the exporter pods.
type ExportConfig struct {
	// Exporter selects the OTEL metrics exporter backend.
	// +kubebuilder:validation:Enum=prometheus;otlp
	// +kubebuilder:default=prometheus
	// +optional
	Exporter string `json:"exporter,omitempty"`

	// OTLPEndpoint is the OTLP metrics endpoint, used when Exporter is "otlp".
	// +optional
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`
}

// ExporterStatus is the shared status for provider CRDs.
type ExporterStatus struct {
	// ObservedGeneration is the .metadata.generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describe the current state of the exporter.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
