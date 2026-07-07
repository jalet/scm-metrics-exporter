package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

func newGitLabReconciler() *GitLabMetricsExporterReconciler {
	return &GitLabMetricsExporterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ExporterImage: testImage}
}

func reconcileGitLab(t *testing.T, name, ns string) {
	t.Helper()
	if _, err := newGitLabReconciler().Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func TestGitLabReconcileCreatesChildren(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gl-creds", map[string][]byte{"token": []byte("glpat")})

	cr := &scmv1alpha1.GitLabMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gl", Namespace: ns},
		Spec: scmv1alpha1.GitLabMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{
				Replicas:          2,
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
	reconcileGitLab(t, "gl", ns)

	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl", Namespace: ns}, &dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.Args) != 1 || c.Args[0] != "--provider=gitlab" {
		t.Errorf("args = %v, want [--provider=gitlab]", c.Args)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Errorf("replicas = %v, want 2", dep.Spec.Replicas)
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
		t.Error("gitlab exporter must not set GITHUB_TOKEN")
	}
	if len(dep.OwnerReferences) != 1 || dep.OwnerReferences[0].Kind != "GitLabMetricsExporter" ||
		dep.OwnerReferences[0].Controller == nil || !*dep.OwnerReferences[0].Controller {
		t.Errorf("owner references = %+v, want a controller ref to the CR", dep.OwnerReferences)
	}

	var svc corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl", Namespace: ns}, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}

	var got scmv1alpha1.GitLabMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady); cond == nil {
		t.Error("Ready condition missing")
	}
}

func TestGitLabReconcileMissingSecretSetsCondition(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)

	cr := &scmv1alpha1.GitLabMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gl-nocreds", Namespace: ns},
		Spec: scmv1alpha1.GitLabMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "absent"}},
			Group:        "acme", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileGitLab(t, "gl-nocreds", ns)

	var got scmv1alpha1.GitLabMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl-nocreds", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCredentialInvalid {
		t.Errorf("Ready condition = %+v, want False/CredentialsInvalid", cond)
	}
	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl-nocreds", Namespace: ns}, &dep); !apierrors.IsNotFound(err) {
		t.Errorf("expected no Deployment when credentials are invalid, got err=%v", err)
	}
}
