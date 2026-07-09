package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
)

func TestGithubEnvLifecycle(t *testing.T) {
	cr := &scmv1alpha1.GitHubMetricsExporter{
		ObjectMeta: metav1.ObjectMeta{Name: "gh", Namespace: "ns"},
		Spec: scmv1alpha1.GitHubMetricsExporterSpec{
			ExporterSpec: scmv1alpha1.ExporterSpec{
				Export:            scmv1alpha1.ExportConfig{OTLPEndpoint: "http://otel:4318"},
				CredentialsSecret: corev1.LocalObjectReference{Name: "creds"},
				CollectLifecycle:  true,
				ResolutionWindow:  metav1.Duration{Duration: 720 * time.Hour},
				Valkey:            &scmv1alpha1.ValkeyConfig{Endpoint: "valkey:6379"},
			},
			Org: "acme", AuthMode: "token", TokenKey: "token",
		},
	}
	env := githubEnv(cr)
	if e, ok := getEnv(env, "SCM_COLLECT_LIFECYCLE"); !ok || e.Value != "true" {
		t.Fatalf("SCM_COLLECT_LIFECYCLE = %+v, ok=%v", e, ok)
	}
	if e, ok := getEnv(env, "SCM_RESOLUTION_WINDOW"); !ok || e.Value != "720h0m0s" {
		t.Fatalf("SCM_RESOLUTION_WINDOW = %+v, ok=%v", e, ok)
	}
	if e, ok := getEnv(env, "VALKEY_ADDR"); !ok || e.Value != "valkey:6379" {
		t.Fatalf("VALKEY_ADDR = %+v, ok=%v", e, ok)
	}
}
