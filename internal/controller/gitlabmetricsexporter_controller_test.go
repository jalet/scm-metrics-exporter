package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

func reconcileGitLab(t *testing.T, name, ns string) {
	t.Helper()
	r := &GitLabMetricsExporterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ExporterImage: testImage}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// GitLab collection is deferred in the discovery/dispatch model: the reconciler dispatches
// nothing and surfaces the deferral as Ready=False/Unsupported.
func TestGitLabReconcileIsDeferred(t *testing.T) {
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
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileGitLab(t, "gl", ns)

	var got scmv1alpha1.GitLabMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gl", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonUnsupported {
		t.Errorf("Ready condition = %+v, want False/Unsupported (GitLab deferred)", cond)
	}
	if len(listJobs(t, ns, "gl")) != 0 {
		t.Error("GitLab reconcile dispatched Jobs, want none (deferred)")
	}
}
