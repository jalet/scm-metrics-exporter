package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-scm-jalet-io-v1alpha1-githubmetricsexporter,mutating=false,failurePolicy=fail,sideEffects=None,groups=scm.jalet.io,resources=githubmetricsexporters,verbs=create;update,versions=v1alpha1,name=vgithubmetricsexporter.scm.jalet.io,admissionReviewVersions=v1

// gitHubValidator rejects a GitHubMetricsExporter whose credentialsSecret is missing or
// lacks the key its auth mode needs -- the cross-object check CEL cannot express.
type gitHubValidator struct {
	reader client.Reader
}

var _ admission.Validator[*GitHubMetricsExporter] = &gitHubValidator{}

// SetupGitHubWebhookWithManager registers the GitHubMetricsExporter validating webhook.
func SetupGitHubWebhookWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy(mgr, &GitHubMetricsExporter{}).
		WithValidator(&gitHubValidator{reader: mgr.GetAPIReader()}).
		Complete()
}

func (v *gitHubValidator) ValidateCreate(ctx context.Context, obj *GitHubMetricsExporter) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

func (v *gitHubValidator) ValidateUpdate(ctx context.Context, _, obj *GitHubMetricsExporter) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

func (v *gitHubValidator) ValidateDelete(context.Context, *GitHubMetricsExporter) (admission.Warnings, error) {
	return nil, nil
}

func (v *gitHubValidator) validate(ctx context.Context, cr *GitHubMetricsExporter) error {
	// Defensive: the API server's CEL already ties authMode to its key field, but the
	// webhook re-derives the required key so it stays correct if CEL ever changes.
	wantKey := cr.Spec.TokenKey
	if cr.Spec.AuthMode == "app" {
		wantKey = cr.Spec.AppPrivateKeyKey
	}
	if wantKey == "" {
		return fmt.Errorf("authMode %q requires its key field (tokenKey for token, appPrivateKeyKey for app)", cr.Spec.AuthMode)
	}
	return validateCredentialsSecret(ctx, v.reader, cr.Namespace, cr.Spec.CredentialsSecret.Name, wantKey)
}
