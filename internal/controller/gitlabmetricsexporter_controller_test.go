package controller

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
	"github.com/jalet/scm-metrics-exporter/internal/discovery"
)

func newGitLabReconciler(repos ...string) *GitLabMetricsExporterReconciler {
	return &GitLabMetricsExporterReconciler{
		Client:        k8sClient,
		Scheme:        k8sClient.Scheme(),
		ExporterImage: testImage,
		DiscoverProjects: func(context.Context, discovery.GitLabAuth, string, string, discovery.Filter) ([]string, error) {
			return repos, nil
		},
	}
}

func reconcileGitLab(t *testing.T, r *GitLabMetricsExporterReconciler, name, ns string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func TestGitLabReconcileDispatchesJobs(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gl-creds", map[string][]byte{"token": []byte("glpat")})

	cr := &scmv1alpha1.GitLabMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: ns},
		Spec: scmv1alpha1.GitLabMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{
				Export:            scmv1alpha1.ExportConfig{OTLPEndpoint: testOTLPEndpoint},
				CredentialsSecret: corev1.LocalObjectReference{Name: "gl-creds"},
			},
			Group:    "acme",
			TokenKey: "token",
			BaseURL:  "https://gitlab.example.com",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileGitLab(t, newGitLabReconciler("acme/svc-a", "acme/svc-b"), "gl", ns)

	jobs := listJobs(t, ns, "gl")
	if len(jobs) != 2 {
		t.Fatalf("dispatched %d jobs, want 2 (one per project)", len(jobs))
	}
	var svcA *batchv1.Job
	for i := range jobs {
		if jobs[i].Name == jobName("gl", "acme/svc-a") {
			svcA = &jobs[i]
		}
	}
	if svcA == nil {
		t.Fatalf("no job for project acme/svc-a; jobs=%v", jobNames(jobs))
	}
	c := svcA.Spec.Template.Spec.Containers[0]
	wantArgs := []string{"--provider=gitlab", "--once", "--repo=acme/svc-a"}
	if got := c.Args; len(got) != 3 || got[0] != wantArgs[0] || got[2] != wantArgs[2] {
		t.Errorf("args = %v, want %v", got, wantArgs)
	}
	if e, ok := getEnv(c.Env, "GITLAB_GROUP"); !ok || e.Value != "acme" {
		t.Errorf("GITLAB_GROUP = %+v, want acme", e)
	}
	if e, ok := getEnv(c.Env, "GITLAB_URL"); !ok || e.Value != "https://gitlab.example.com" {
		t.Errorf("GITLAB_URL = %+v", e)
	}
	tok, ok := getEnv(c.Env, "GITLAB_TOKEN")
	if !ok || tok.ValueFrom == nil || tok.ValueFrom.SecretKeyRef == nil ||
		tok.ValueFrom.SecretKeyRef.Name != "gl-creds" || tok.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("GITLAB_TOKEN = %+v, want secretKeyRef gl-creds/token", tok)
	}
	if _, ok := getEnv(c.Env, "GITHUB_TOKEN"); ok {
		t.Error("gitlab job must not set GITHUB_TOKEN")
	}
	if len(svcA.OwnerReferences) != 1 || svcA.OwnerReferences[0].Kind != "GitLabMetricsExporter" {
		t.Errorf("owner references = %+v, want a ref to the CR", svcA.OwnerReferences)
	}

	var got scmv1alpha1.GitLabMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.DiscoveredRepositories) != 2 {
		t.Errorf("discoveredRepositories = %v, want 2", got.Status.DiscoveredRepositories)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want True", cond)
	}
}

func TestGitLabReconcileMissingSecretSetsCondition(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)

	cr := &scmv1alpha1.GitLabMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gl-nocreds", Namespace: ns},
		Spec: scmv1alpha1.GitLabMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{
				Export:            scmv1alpha1.ExportConfig{OTLPEndpoint: testOTLPEndpoint},
				CredentialsSecret: corev1.LocalObjectReference{Name: "absent"},
			},
			Group: "acme", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileGitLab(t, newGitLabReconciler("acme/svc-a"), "gl-nocreds", ns)

	var got scmv1alpha1.GitLabMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl-nocreds", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCredentialInvalid {
		t.Errorf("Ready condition = %+v, want False/CredentialsInvalid", cond)
	}
	if len(listJobs(t, ns, "gl-nocreds")) != 0 {
		t.Error("dispatched Jobs with invalid credentials, want none")
	}
}
