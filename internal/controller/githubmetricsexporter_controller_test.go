package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

const testImage = "ghcr.io/jalet/scm-metrics-exporter:test"

var k8sClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "skipping controller envtest: KUBEBUILDER_ASSETS not set (run `mise run test:envtest`)")
		os.Exit(0)
	}
	os.Exit(runEnvtest(m))
}

func runEnvtest(m *testing.M) int {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			"testdata", // minimal stand-in ServiceMonitor CRD
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		panic(fmt.Sprintf("start envtest: %v", err))
	}
	defer func() { _ = testEnv.Stop() }()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(scmv1alpha1.AddToScheme(scheme))

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(fmt.Sprintf("build client: %v", err))
	}
	return m.Run()
}

func newReconciler() *GitHubMetricsExporterReconciler {
	return &GitHubMetricsExporterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ExporterImage: testImage}
}

func createNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-"}}
	if err := k8sClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), ns) })
	return ns.Name
}

func createSecret(t *testing.T, ns, name string, data map[string][]byte) {
	t.Helper()
	if err := k8sClient.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       data,
	}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
}

func reconcile(t *testing.T, name, ns string) {
	t.Helper()
	if _, err := newReconciler().Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getEnv(env []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, e := range env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func TestGitHubTargetTypeCELValidation(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)

	bad := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-baduser", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "c"}},
			TargetType:   "user", AuthMode: "token", TokenKey: "token", // targetType=user but no user
		},
	}
	if err := k8sClient.Create(ctx, bad); err == nil {
		t.Fatal("create user CR without user: got nil error, want CEL rejection")
	}

	good := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-gooduser", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "c"}},
			TargetType:   "user", User: "octocat", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, good); err != nil {
		t.Fatalf("create valid user CR: %v", err)
	}
}

func TestReconcileUserTargetEnv(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "ghu", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "gh-creds"}},
			TargetType:   "user", User: "octocat", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcile(t, "ghu", ns)

	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "ghu", Namespace: ns}, &dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	env := dep.Spec.Template.Spec.Containers[0].Env
	if e, ok := getEnv(env, "GITHUB_TARGET_TYPE"); !ok || e.Value != "user" {
		t.Errorf("GITHUB_TARGET_TYPE = %q (found=%v), want user", e.Value, ok)
	}
	if e, ok := getEnv(env, "GITHUB_USER"); !ok || e.Value != "octocat" {
		t.Errorf("GITHUB_USER = %q (found=%v), want octocat", e.Value, ok)
	}
}

func TestReconcileTokenModeCreatesChildren(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{
				Replicas:          2,
				CredentialsSecret: corev1.LocalObjectReference{Name: "gh-creds"},
			},
			Org: "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcile(t, "gh", ns)

	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh", Namespace: ns}, &dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if c.Image != testImage {
		t.Errorf("image = %q, want %q", c.Image, testImage)
	}
	if len(c.Command) != 1 || c.Command[0] != "/exporter" {
		t.Errorf("command = %v, want [/exporter]", c.Command)
	}
	if len(c.Args) != 1 || c.Args[0] != "--provider=github" {
		t.Errorf("args = %v, want [--provider=github]", c.Args)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Errorf("replicas = %v, want 2", dep.Spec.Replicas)
	}
	if e, ok := getEnv(c.Env, "GITHUB_ORG"); !ok || e.Value != "acme" {
		t.Errorf("GITHUB_ORG = %+v, want acme", e)
	}
	tok, ok := getEnv(c.Env, "GITHUB_TOKEN")
	if !ok || tok.ValueFrom == nil || tok.ValueFrom.SecretKeyRef == nil ||
		tok.ValueFrom.SecretKeyRef.Name != "gh-creds" || tok.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("GITHUB_TOKEN = %+v, want secretKeyRef gh-creds/token", tok)
	}
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("container securityContext.readOnlyRootFilesystem not set")
	}
	if len(dep.OwnerReferences) != 1 || dep.OwnerReferences[0].Kind != "GitHubMetricsExporter" ||
		dep.OwnerReferences[0].Controller == nil || !*dep.OwnerReferences[0].Controller {
		t.Errorf("owner references = %+v, want a controller ref to the CR", dep.OwnerReferences)
	}

	var svc corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh", Namespace: ns}, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != metricsPort {
		t.Errorf("service ports = %+v, want one port %d", svc.Spec.Ports, metricsPort)
	}

	var got scmv1alpha1.GitHubMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady); cond == nil {
		t.Error("Ready condition missing")
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}

func TestReconcileAppModeMountsKey(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-app", map[string][]byte{"key.pem": []byte("PEM")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-app", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec:      scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "gh-app"}},
			Org:               "acme",
			AuthMode:          "app",
			AppID:             12,
			AppInstallationID: 34,
			AppPrivateKeyKey:  "key.pem",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcile(t, "gh-app", ns)

	var dep appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-app", Namespace: ns}, &dep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != appPEMMountPath || !c.VolumeMounts[0].ReadOnly {
		t.Errorf("volume mounts = %+v, want read-only mount at %s", c.VolumeMounts, appPEMMountPath)
	}
	if len(dep.Spec.Template.Spec.Volumes) != 1 || dep.Spec.Template.Spec.Volumes[0].Secret == nil ||
		dep.Spec.Template.Spec.Volumes[0].Secret.SecretName != "gh-app" {
		t.Errorf("volumes = %+v, want secret volume gh-app", dep.Spec.Template.Spec.Volumes)
	}
	if e, ok := getEnv(c.Env, "GITHUB_APP_PRIVATE_KEY_PATH"); !ok || e.Value != appPEMMountPath+"/"+appPEMFileName {
		t.Errorf("GITHUB_APP_PRIVATE_KEY_PATH = %+v", e)
	}
	if e, ok := getEnv(c.Env, "GITHUB_APP_ID"); !ok || e.Value != "12" {
		t.Errorf("GITHUB_APP_ID = %+v, want 12", e)
	}
	if _, ok := getEnv(c.Env, "GITHUB_TOKEN"); ok {
		t.Error("app mode must not set GITHUB_TOKEN")
	}
}

func TestReconcileMissingSecretSetsCondition(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-nocreds", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "absent"}},
			Org:          "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcile(t, "gh-nocreds", ns)

	var got scmv1alpha1.GitHubMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-nocreds", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCredentialInvalid {
		t.Errorf("Ready condition = %+v, want False/CredentialsInvalid", cond)
	}

	var dep appsv1.Deployment
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-nocreds", Namespace: ns}, &dep)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected no Deployment when credentials are invalid, got err=%v", err)
	}
}
