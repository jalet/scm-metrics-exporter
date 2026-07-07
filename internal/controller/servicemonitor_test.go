package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

// reconcileSM reconciles with the ServiceMonitor availability flag set explicitly
// (the tests drive Reconcile directly, without SetupWithManager).
func reconcileSM(t *testing.T, name, ns string, available bool) {
	t.Helper()
	rec := newReconciler()
	rec.serviceMonitorAvailable = available
	if _, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func newGitHubCRWithServiceMonitor(name, ns string) *scmv1alpha1.GitHubMetricsExporter {
	return &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{
				ServiceMonitor:    true,
				CredentialsSecret: corev1.LocalObjectReference{Name: "gh-creds"},
			},
			Org: "acme", AuthMode: "token", TokenKey: "token",
		},
	}
}

func TestServiceMonitorInstalledDetectsStandinCRD(t *testing.T) {
	ok, err := serviceMonitorInstalled(k8sClient.RESTMapper())
	if err != nil {
		t.Fatalf("serviceMonitorInstalled: %v", err)
	}
	if !ok {
		t.Fatal("expected the stand-in ServiceMonitor CRD to be detected via the RESTMapper")
	}
}

func TestReconcileServiceMonitorCreatedWhenEnabled(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})
	if err := k8sClient.Create(ctx, newGitHubCRWithServiceMonitor("gh-sm", ns)); err != nil {
		t.Fatalf("create cr: %v", err)
	}

	reconcileSM(t, "gh-sm", ns, true)

	sm := newServiceMonitor()
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-sm", Namespace: ns}, sm); err != nil {
		t.Fatalf("get servicemonitor: %v", err)
	}
	ml, _, _ := unstructured.NestedStringMap(sm.Object, "spec", "selector", "matchLabels")
	if want := selectorLabels("gh-sm"); !reflect.DeepEqual(ml, want) {
		t.Errorf("selector.matchLabels = %v, want %v", ml, want)
	}
	eps, _, _ := unstructured.NestedSlice(sm.Object, "spec", "endpoints")
	if len(eps) != 1 {
		t.Fatalf("endpoints = %v, want one entry", eps)
	}
	if port, _, _ := unstructured.NestedString(eps[0].(map[string]any), "port"); port != metricsPortName {
		t.Errorf("endpoint port = %q, want %q", port, metricsPortName)
	}
	owners := sm.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "GitHubMetricsExporter" ||
		owners[0].Controller == nil || !*owners[0].Controller {
		t.Errorf("owner references = %+v, want a controller ref to the CR", owners)
	}
}

func TestReconcileServiceMonitorDeletedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})
	if err := k8sClient.Create(ctx, newGitHubCRWithServiceMonitor("gh-sm", ns)); err != nil {
		t.Fatalf("create cr: %v", err)
	}

	reconcileSM(t, "gh-sm", ns, true) // create it

	// Disable and reconcile again. envtest has no garbage collector, so this exercises
	// the operator's explicit delete-if-disabled path, not an owner-ref cascade.
	var cr scmv1alpha1.GitHubMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-sm", Namespace: ns}, &cr); err != nil {
		t.Fatal(err)
	}
	cr.Spec.ServiceMonitor = false
	if err := k8sClient.Update(ctx, &cr); err != nil {
		t.Fatalf("update cr: %v", err)
	}
	reconcileSM(t, "gh-sm", ns, true)

	sm := newServiceMonitor()
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-sm", Namespace: ns}, sm)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected ServiceMonitor to be deleted, got err=%v", err)
	}
}

func TestReconcileServiceMonitorSkippedWhenCRDAbsent(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})
	if err := k8sClient.Create(ctx, newGitHubCRWithServiceMonitor("gh-sm-absent", ns)); err != nil {
		t.Fatalf("create cr: %v", err)
	}

	// available=false simulates a cluster without prometheus-operator: reconcile must
	// succeed and create no ServiceMonitor.
	reconcileSM(t, "gh-sm-absent", ns, false)

	sm := newServiceMonitor()
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-sm-absent", Namespace: ns}, sm)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected no ServiceMonitor when availability is false, got err=%v", err)
	}
}
