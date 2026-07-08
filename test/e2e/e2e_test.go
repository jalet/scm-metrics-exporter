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
			ExporterSpec: scmv1alpha1.ExporterSpec{
				Export:            scmv1alpha1.ExportConfig{OTLPEndpoint: "http://otel-collector.observability:4318"},
				CredentialsSecret: corev1.LocalObjectReference{Name: "creds"},
			},
			Org: "octo-org", AuthMode: "token", TokenKey: "token",
		},
	}); err != nil {
		t.Fatalf("create cr: %v", err)
	}

	// The operator must reconcile the CR end-to-end: stamp observedGeneration and a Ready
	// condition. (With the fake token, live discovery fails, so Ready is False/DiscoveryFailed
	// -- which still proves the operator watched and processed the CR without needing real
	// GitHub credentials or a network mock.)
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		var got scmv1alpha1.GitHubMetricsExporter
		if err := c.Get(ctx, types.NamespacedName{Name: "gh", Namespace: ns.Name}, &got); err != nil {
			return false, nil //nolint:nilerr // not observed yet; keep polling
		}
		return got.Status.ObservedGeneration == got.Generation && len(got.Status.Conditions) > 0, nil
	})
	if err != nil {
		t.Fatalf("operator did not reconcile the CR: %v", err)
	}
}
