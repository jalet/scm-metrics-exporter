package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GitHubMetricsExporterSpec configures a GitHub metrics exporter.
//
// +kubebuilder:validation:XValidation:rule="self.targetType != 'org' || (has(self.org) && size(self.org) > 0)",message="targetType 'org' requires org"
// +kubebuilder:validation:XValidation:rule="self.targetType != 'user' || (has(self.user) && size(self.user) > 0)",message="targetType 'user' requires user"
// +kubebuilder:validation:XValidation:rule="self.authMode != 'app' || (has(self.appID) && self.appID > 0 && has(self.appInstallationID) && self.appInstallationID > 0 && has(self.appPrivateKeyKey) && size(self.appPrivateKeyKey) > 0)",message="authMode 'app' requires appID, appInstallationID, and appPrivateKeyKey"
// +kubebuilder:validation:XValidation:rule="self.authMode != 'token' || (has(self.tokenKey) && size(self.tokenKey) > 0)",message="authMode 'token' requires tokenKey"
type GitHubMetricsExporterSpec struct {
	ExporterSpec `json:",inline"`

	// TargetType selects whether Org or User is polled.
	// +kubebuilder:validation:Enum=org;user
	// +kubebuilder:default=org
	// +optional
	TargetType string `json:"targetType,omitempty"`

	// Org is the GitHub organization to poll (targetType "org").
	// +optional
	Org string `json:"org,omitempty"`

	// User is the GitHub user account to poll (targetType "user"). Code scanning is
	// gathered per-repository for a user (GitHub has no org-scoped user endpoint).
	// +optional
	User string `json:"user,omitempty"`

	// AuthMode selects the credential type held in CredentialsSecret.
	// +kubebuilder:validation:Enum=token;app
	// +kubebuilder:default=token
	AuthMode string `json:"authMode"`

	// TokenKey is the CredentialsSecret key holding a PAT (authMode "token").
	// +optional
	TokenKey string `json:"tokenKey,omitempty"`

	// AppID is the GitHub App ID (authMode "app").
	// +optional
	AppID int64 `json:"appID,omitempty"`

	// AppInstallationID is the GitHub App installation ID (authMode "app").
	// +optional
	AppInstallationID int64 `json:"appInstallationID,omitempty"`

	// AppPrivateKeyKey is the CredentialsSecret key holding the App private key PEM
	// (authMode "app").
	// +optional
	AppPrivateKeyKey string `json:"appPrivateKeyKey,omitempty"`

	// CodeScanningTool optionally filters code scanning alerts to one SARIF tool
	// (for example "CodeQL"). Empty counts all tools.
	// +optional
	CodeScanningTool string `json:"codeScanningTool,omitempty"`

	// CollectWorkflows enables recent GitHub Actions workflow-run metrics
	// (scm_workflow_runs_recent), tallied per collection Job. Off by default because it
	// adds an API call per repository and extra series cardinality.
	// +optional
	CollectWorkflows bool `json:"collectWorkflows,omitempty"`

	// WorkflowLookback bounds how far back workflow runs are counted (used when
	// CollectWorkflows is true).
	// +kubebuilder:default="168h"
	// +optional
	WorkflowLookback metav1.Duration `json:"workflowLookback,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ghme
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.targetType`
// +kubebuilder:printcolumn:name="Org",type=string,JSONPath=`.spec.org`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="Auth",type=string,JSONPath=`.spec.authMode`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GitHubMetricsExporter is the Schema for the githubmetricsexporters API.
type GitHubMetricsExporter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitHubMetricsExporterSpec `json:"spec,omitempty"`
	Status ExporterStatus            `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GitHubMetricsExporterList contains a list of GitHubMetricsExporter.
type GitHubMetricsExporterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitHubMetricsExporter `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &GitHubMetricsExporter{}, &GitHubMetricsExporterList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
}
