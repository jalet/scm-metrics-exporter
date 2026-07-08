package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/jalet/scm-metrics-exporter/internal/discovery"
)

const (
	testImage        = "ghcr.io/jalet/scm-metrics-exporter:test"
	testOTLPEndpoint = "http://otel-collector:4318"
)

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
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
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

// stubDiscover returns a fixed repository list, so reconciliation needs no network.
func stubDiscover(repos ...string) func(context.Context, discovery.GitHubAuth, string, string, discovery.Filter) ([]string, error) {
	return func(context.Context, discovery.GitHubAuth, string, string, discovery.Filter) ([]string, error) {
		return repos, nil
	}
}

func newReconciler(repos ...string) *GitHubMetricsExporterReconciler {
	return &GitHubMetricsExporterReconciler{
		Client:        k8sClient,
		Scheme:        k8sClient.Scheme(),
		ExporterImage: testImage,
		DiscoverRepos: stubDiscover(repos...),
	}
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

func reconcileWith(t *testing.T, r *GitHubMetricsExporterReconciler, name, ns string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func listJobs(t *testing.T, ns, crName string) []batchv1.Job {
	t.Helper()
	var jobs batchv1.JobList
	if err := k8sClient.List(context.Background(), &jobs, client.InNamespace(ns), client.MatchingLabels(selectorLabels(crName))); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	return jobs.Items
}

func getEnv(env []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, e := range env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func baseSpec(secretName string) scmv1alpha1.ExporterSpec {
	return scmv1alpha1.ExporterSpec{
		Export:            scmv1alpha1.ExportConfig{OTLPEndpoint: testOTLPEndpoint},
		CredentialsSecret: corev1.LocalObjectReference{Name: secretName},
	}
}

func TestGitHubTargetTypeCELValidation(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)

	bad := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-baduser", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: baseSpec("c"),
			TargetType:   "user", AuthMode: "token", TokenKey: "token", // targetType=user but no user
		},
	}
	if err := k8sClient.Create(ctx, bad); err == nil {
		t.Fatal("create user CR without user: got nil error, want CEL rejection")
	}

	good := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-gooduser", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: baseSpec("c"),
			TargetType:   "user", User: "octocat", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, good); err != nil {
		t.Fatalf("create valid user CR: %v", err)
	}
}

func TestOTLPEndpointRequired(t *testing.T) {
	ns := createNamespace(t)
	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-noendpoint", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{CredentialsSecret: corev1.LocalObjectReference{Name: "c"}}, // no export.otlpEndpoint
			Org:          "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(context.Background(), cr); err == nil {
		t.Fatal("create CR without export.otlpEndpoint: got nil error, want rejection")
	}
}

func TestReconcileDispatchesOneJobPerRepo(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: baseSpec("gh-creds"),
			Org:          "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileWith(t, newReconciler("alpha", "beta", "gamma"), "gh", ns)

	jobs := listJobs(t, ns, "gh")
	if len(jobs) != 3 {
		t.Fatalf("dispatched %d jobs, want 3 (one per repo)", len(jobs))
	}

	// Inspect the alpha job.
	var alpha *batchv1.Job
	for i := range jobs {
		if jobs[i].Name == jobName("gh", "alpha") {
			alpha = &jobs[i]
		}
	}
	if alpha == nil {
		t.Fatalf("no job for repo alpha; jobs=%v", jobNames(jobs))
	}
	c := alpha.Spec.Template.Spec.Containers[0]
	if c.Image != testImage {
		t.Errorf("image = %q, want %q", c.Image, testImage)
	}
	wantArgs := []string{"--provider=github", "--once", "--repo=alpha"}
	if fmt.Sprint(c.Args) != fmt.Sprint(wantArgs) {
		t.Errorf("args = %v, want %v", c.Args, wantArgs)
	}
	if alpha.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", alpha.Spec.Template.Spec.RestartPolicy)
	}
	if e, ok := getEnv(c.Env, "GITHUB_ORG"); !ok || e.Value != "acme" {
		t.Errorf("GITHUB_ORG = %+v, want acme", e)
	}
	if e, ok := getEnv(c.Env, "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); !ok || e.Value != testOTLPEndpoint {
		t.Errorf("OTLP endpoint env = %+v, want %s", e, testOTLPEndpoint)
	}
	tok, ok := getEnv(c.Env, "GITHUB_TOKEN")
	if !ok || tok.ValueFrom == nil || tok.ValueFrom.SecretKeyRef == nil ||
		tok.ValueFrom.SecretKeyRef.Name != "gh-creds" || tok.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("GITHUB_TOKEN = %+v, want secretKeyRef gh-creds/token", tok)
	}
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("container securityContext.readOnlyRootFilesystem not set")
	}
	if len(alpha.OwnerReferences) != 1 || alpha.OwnerReferences[0].Kind != "GitHubMetricsExporter" ||
		alpha.OwnerReferences[0].Controller == nil || !*alpha.OwnerReferences[0].Controller {
		t.Errorf("owner references = %+v, want a controller ref to the CR", alpha.OwnerReferences)
	}

	var got scmv1alpha1.GitHubMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.DiscoveredRepositories) != 3 {
		t.Errorf("discoveredRepositories = %v, want 3", got.Status.DiscoveredRepositories)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want True", cond)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}

func TestReconcileParallelismCap(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-cap", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: func() scmv1alpha1.ExporterSpec { s := baseSpec("gh-creds"); s.Parallelism = 2; return s }(),
			Org:          "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	// 5 repos, parallelism 2: only 2 Jobs should be created this pass (the rest wait for
	// running Jobs to finish; envtest does not run pods, so they stay active).
	reconcileWith(t, newReconciler("r1", "r2", "r3", "r4", "r5"), "gh-cap", ns)

	if jobs := listJobs(t, ns, "gh-cap"); len(jobs) != 2 {
		t.Fatalf("dispatched %d jobs, want 2 (parallelism cap)", len(jobs))
	}
}

func TestReconcileUserTargetEnv(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-creds", map[string][]byte{"token": []byte("ghp_x")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "ghu", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: baseSpec("gh-creds"),
			TargetType:   "user", User: "octocat", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileWith(t, newReconciler("solo"), "ghu", ns)

	jobs := listJobs(t, ns, "ghu")
	if len(jobs) != 1 {
		t.Fatalf("dispatched %d jobs, want 1", len(jobs))
	}
	env := jobs[0].Spec.Template.Spec.Containers[0].Env
	if e, ok := getEnv(env, "GITHUB_TARGET_TYPE"); !ok || e.Value != "user" {
		t.Errorf("GITHUB_TARGET_TYPE = %q (found=%v), want user", e.Value, ok)
	}
	if e, ok := getEnv(env, "GITHUB_USER"); !ok || e.Value != "octocat" {
		t.Errorf("GITHUB_USER = %q (found=%v), want octocat", e.Value, ok)
	}
}

func TestReconcileAppModeMountsKey(t *testing.T) {
	ctx := context.Background()
	ns := createNamespace(t)
	createSecret(t, ns, "gh-app", map[string][]byte{"key.pem": []byte("PEM")})

	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-app", Namespace: ns},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec:      baseSpec("gh-app"),
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
	reconcileWith(t, newReconciler("repoA"), "gh-app", ns)

	jobs := listJobs(t, ns, "gh-app")
	if len(jobs) != 1 {
		t.Fatalf("dispatched %d jobs, want 1", len(jobs))
	}
	pod := jobs[0].Spec.Template.Spec
	c := pod.Containers[0]
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != appPEMMountPath || !c.VolumeMounts[0].ReadOnly {
		t.Errorf("volume mounts = %+v, want read-only mount at %s", c.VolumeMounts, appPEMMountPath)
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].Secret == nil || pod.Volumes[0].Secret.SecretName != "gh-app" {
		t.Errorf("volumes = %+v, want secret volume gh-app", pod.Volumes)
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
			ExporterSpec: baseSpec("absent"),
			Org:          "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	if err := k8sClient.Create(ctx, cr); err != nil {
		t.Fatalf("create cr: %v", err)
	}
	reconcileWith(t, newReconciler("alpha"), "gh-nocreds", ns)

	var got scmv1alpha1.GitHubMetricsExporter
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gh-nocreds", Namespace: ns}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCredentialInvalid {
		t.Errorf("Ready condition = %+v, want False/CredentialsInvalid", cond)
	}
	if jobs := listJobs(t, ns, "gh-nocreds"); len(jobs) != 0 {
		t.Errorf("dispatched %d jobs, want 0 when credentials are invalid", len(jobs))
	}
}

func jobNames(jobs []batchv1.Job) []string {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	return names
}
