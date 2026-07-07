package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GitLabMetricsExporterSpec configures a GitLab metrics exporter.
type GitLabMetricsExporterSpec struct {
	ExporterSpec `json:",inline"`

	// Group is the GitLab group to poll.
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`

	// TokenKey is the CredentialsSecret key holding the GitLab access token.
	// +kubebuilder:validation:MinLength=1
	TokenKey string `json:"tokenKey"`

	// BaseURL is the GitLab API base URL for a self-hosted instance. Empty targets
	// gitlab.com.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=glme
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.group`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GitLabMetricsExporter is the Schema for the gitlabmetricsexporters API.
type GitLabMetricsExporter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitLabMetricsExporterSpec `json:"spec,omitempty"`
	Status ExporterStatus            `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GitLabMetricsExporterList contains a list of GitLabMetricsExporter.
type GitLabMetricsExporterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitLabMetricsExporter `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &GitLabMetricsExporter{}, &GitLabMetricsExporterList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
}
