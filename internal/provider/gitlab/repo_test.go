package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const (
	projectVulnsJSON = `{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"severity":"HIGH","reportType":"DEPENDENCY_SCANNING","scanner":{"name":"gemnasium"},"detectedAt":"2024-01-02T03:04:05Z"},
		{"severity":"CRITICAL","reportType":"SAST","scanner":{"name":"semgrep"},"detectedAt":"2024-01-02T03:04:05Z"}
	]}}}}`
	projectPostureJSON = `{"data":{"project":{"visibility":"private","archived":false,
		"securityScanners":{"enabled":["SAST","DEPENDENCY_SCANNING","SECRET_DETECTION"]},
		"branchRules":{"nodes":[{"isDefault":true,"isProtected":true}]}}}}`
)

func TestSnapshotRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			w.Header().Set("RateLimit-Remaining", "1990")
			w.Header().Set("Content-Type", "application/json")
			query, _ := readGraphQL(t, r)
			if strings.Contains(query, "ProjectPosture") {
				_, _ = w.Write([]byte(projectPostureJSON))
				return
			}
			_, _ = w.Write([]byte(projectVulnsJSON))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/merge_requests"):
			w.Header().Set("RateLimit-Remaining", "59")
			_, _ = w.Write([]byte(`[{},{},{}]`)) // 3 open MRs
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{}).SnapshotRepo(context.Background(), "acme", "acme/svc")
	if err != nil {
		t.Fatalf("SnapshotRepo: %v", err)
	}
	want := provider.Snapshot{
		Repos: []provider.RepoMetrics{{
			Name:            "acme/svc",
			OpenReviewItems: 3,
			Posture:         &provider.RepoPosture{Visibility: "private", DependabotEnabled: true, BranchProtected: true, SecretScanningEnabled: true},
			Findings: []provider.Finding{
				{Severity: "high", Category: "dependency", Tool: "gemnasium", CreatedAt: parseGitLabTime("2024-01-02T03:04:05Z")},
				{Severity: "critical", Category: "static_analysis", Tool: "semgrep", CreatedAt: parseGitLabTime("2024-01-02T03:04:05Z")},
			},
		}},
		RateLimits: []provider.RateLimit{
			{Resource: "rest", Remaining: 59},
			{Resource: "graphql", Remaining: 1990},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("snapshot mismatch (-want +got):\n%s", diff)
	}
}

func TestSnapshotRepoPipelines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			w.Header().Set("Content-Type", "application/json")
			query, _ := readGraphQL(t, r)
			if strings.Contains(query, "ProjectPosture") {
				_, _ = w.Write([]byte(projectPostureJSON))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`))
		case strings.Contains(r.URL.Path, "/pipelines"):
			_, _ = w.Write([]byte(`[
				{"status":"success","source":"push"},
				{"status":"failed","source":"push"},
				{"status":"success","source":"schedule"},
				{"status":"running","source":"push"}
			]`))
		case strings.Contains(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{}]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{CollectWorkflows: true}).SnapshotRepo(context.Background(), "acme", "acme/svc")
	if err != nil {
		t.Fatalf("SnapshotRepo: %v", err)
	}
	want := []provider.WorkflowRunStat{
		{Workflow: "push", Conclusion: "failed", Count: 1},
		{Workflow: "push", Conclusion: "success", Count: 1},
		{Workflow: "schedule", Conclusion: "success", Count: 1},
	}
	if diff := cmp.Diff(want, got.Repos[0].WorkflowRuns); diff != "" {
		t.Errorf("pipeline runs mismatch (-want +got):\n%s\n(running pipelines must be skipped)", diff)
	}
}

func TestSnapshotRepoVulnsUnavailableIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			w.Header().Set("Content-Type", "application/json")
			query, _ := readGraphQL(t, r)
			if strings.Contains(query, "ProjectPosture") {
				_, _ = w.Write([]byte(projectPostureJSON))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"project":{"vulnerabilities":null}}}`)) // non-Ultimate
		case strings.Contains(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{}).SnapshotRepo(context.Background(), "acme", "acme/svc")
	if err != nil {
		t.Fatalf("SnapshotRepo returned a hard error, want partial: %v", err)
	}
	if len(got.Repos) != 1 || got.Repos[0].OpenReviewItems != 1 || got.Repos[0].Posture == nil {
		t.Fatalf("repos = %+v, want the project with MRs + posture", got.Repos)
	}
	if len(got.Repos[0].Findings) != 0 {
		t.Errorf("findings = %+v, want none (vulnerabilities unavailable)", got.Repos[0].Findings)
	}
	found := false
	for _, se := range got.SourceErrors {
		if se.Source == provider.SourceGraphQL {
			found = true
		}
	}
	if !found {
		t.Errorf("SourceErrors = %+v, want graphql", got.SourceErrors)
	}
}
