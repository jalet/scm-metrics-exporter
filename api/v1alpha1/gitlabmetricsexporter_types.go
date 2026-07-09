package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GitLabMetricsExporterSpec configures a GitLab metrics exporter.
//
// +kubebuilder:validation:XValidation:rule="self.targetType != 'group' || (has(self.group) && size(self.group) > 0)",message="targetType 'group' requires group"
// +kubebuilder:validation:XValidation:rule="self.targetType != 'user' || (has(self.user) && size(self.user) > 0)",message="targetType 'user' requires user"
// +kubebuilder:validation:XValidation:rule="!self.collectLifecycle || has(self.valkey)",message="valkey is required when collectLifecycle is true"
type GitLabMetricsExporterSpec struct {
	ExporterSpec `json:",inline"`

	// TargetType selects whether Group or User is polled. User targets yield merge
	// request counts only; security findings are unavailable (GitLab vulnerabilities
	// are Ultimate/group-scoped).
	// +kubebuilder:validation:Enum=group;user
	// +kubebuilder:default=group
	// +optional
	TargetType string `json:"targetType,omitempty"`

	// Group is the GitLab group to poll (targetType "group").
	// +optional
	Group string `json:"group,omitempty"`

	// User is the GitLab user namespace to poll (targetType "user").
	// +optional
	User string `json:"user,omitempty"`

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
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.targetType`
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.group`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
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
