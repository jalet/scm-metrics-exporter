package v1alpha1

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetupWebhooksWithManager registers the validating admission webhooks for both CRDs.
// The manager's webhook server must have serving certs available; callers that cannot
// guarantee that (local runs, envtest without WebhookInstallOptions) should skip this.
func SetupWebhooksWithManager(mgr ctrl.Manager, watchNamespaces []string) error {
	if err := SetupGitHubWebhookWithManager(mgr, watchNamespaces); err != nil {
		return err
	}
	return SetupGitLabWebhookWithManager(mgr, watchNamespaces)
}

// namespaceSet holds the namespaces the operator watches; empty means every namespace.
type namespaceSet map[string]struct{}

func newNamespaceSet(namespaces []string) namespaceSet {
	set := make(namespaceSet, len(namespaces))
	for _, ns := range namespaces {
		set[ns] = struct{}{}
	}
	return set
}

// validateWatched rejects a CR outside the watch list, which the operator would never reconcile.
func (s namespaceSet) validateWatched(namespace string) error {
	if len(s) == 0 {
		return nil
	}
	if _, ok := s[namespace]; ok {
		return nil
	}
	return fmt.Errorf("namespace %q is not watched by this operator (watchNamespaces: %s)", namespace, s)
}

func (s namespaceSet) String() string {
	names := make([]string, 0, len(s))
	for ns := range s {
		names = append(names, ns)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
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
