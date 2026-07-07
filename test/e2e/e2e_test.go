// Package e2e contains an end-to-end test that runs against a real cluster with the
// operator already installed (for example via `helm install`). It is gated behind
// RUN_E2E=1 so it is skipped by the normal unit-test run; invoke it with
// `mise run test:e2e` against a kind cluster that has the chart installed.
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

func TestGitHubExporterReconciledEndToEnd(t *testing.T) {
	if os.Getenv("RUN_E2E") != "1" {
		t.Skip("set RUN_E2E=1 (and point KUBECONFIG at a cluster with the operator installed) to run the e2e test")
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(scmv1alpha1.AddToScheme(scheme))

	cfg, err := config.GetConfig()
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "scm-e2e-"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), ns) })

	if err := c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: ns.Name},
		StringData: map[string]string{"token": "fake-token-for-e2e"},
	}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := c.Create(ctx, &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: ns.Name},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "creds"}},
			Org:          "octo-org", AuthMode: "token", TokenKey: "token",
		},
	}); err != nil {
		t.Fatalf("create cr: %v", err)
	}

	// The operator must create the exporter Deployment and it must become available.
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		var dep appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Name: "gh", Namespace: ns.Name}, &dep); err != nil {
			return false, nil //nolint:nilerr // not created yet; keep polling
		}
		for _, cond := range dep.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("exporter deployment did not become available: %v", err)
	}
}
