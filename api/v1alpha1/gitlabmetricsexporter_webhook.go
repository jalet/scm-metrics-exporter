package v1alpha1

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-scm-jalet-io-v1alpha1-gitlabmetricsexporter,mutating=false,failurePolicy=fail,sideEffects=None,groups=scm.jalet.io,resources=gitlabmetricsexporters,verbs=create;update,versions=v1alpha1,name=vgitlabmetricsexporter.scm.jalet.io,admissionReviewVersions=v1

// gitLabValidator rejects a GitLabMetricsExporter whose credentialsSecret is missing or
// lacks the tokenKey -- the cross-object check CEL cannot express.
type gitLabValidator struct {
	reader client.Reader
}

var _ admission.Validator[*GitLabMetricsExporter] = &gitLabValidator{}

// SetupGitLabWebhookWithManager registers the GitLabMetricsExporter validating webhook.
func SetupGitLabWebhookWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy(mgr, &GitLabMetricsExporter{}).
		WithValidator(&gitLabValidator{reader: mgr.GetAPIReader()}).
		Complete()
}

func (v *gitLabValidator) ValidateCreate(ctx context.Context, obj *GitLabMetricsExporter) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

func (v *gitLabValidator) ValidateUpdate(ctx context.Context, _, obj *GitLabMetricsExporter) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

func (v *gitLabValidator) ValidateDelete(context.Context, *GitLabMetricsExporter) (admission.Warnings, error) {
	return nil, nil
}

func (v *gitLabValidator) validate(ctx context.Context, cr *GitLabMetricsExporter) error {
	return validateCredentialsSecret(ctx, v.reader, cr.Namespace, cr.Spec.CredentialsSecret.Name, cr.Spec.TokenKey)
}
