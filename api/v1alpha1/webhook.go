package v1alpha1

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetupWebhooksWithManager registers the validating admission webhooks for both CRDs.
// The manager's webhook server must have serving certs available; callers that cannot
// guarantee that (local runs, envtest without WebhookInstallOptions) should skip this.
func SetupWebhooksWithManager(mgr ctrl.Manager) error {
	if err := SetupGitHubWebhookWithManager(mgr); err != nil {
		return err
	}
	return SetupGitLabWebhookWithManager(mgr)
}

// validateCredentialsSecret rejects a CR whose referenced Secret is missing or lacks the
// required key. This is the cross-object check the API server's CEL rules cannot perform,
// so it is the reason the webhook exists. reader is an uncached API reader.
func validateCredentialsSecret(ctx context.Context, reader client.Reader, namespace, name, key string) error {
	if name == "" {
		return fmt.Errorf("spec.credentialsSecret.name is required")
	}
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("credentialsSecret %q not found in namespace %q", name, namespace)
		}
		return err
	}
	if _, ok := secret.Data[key]; !ok {
		return fmt.Errorf("credentialsSecret %q is missing key %q", name, key)
	}
	return nil
}
