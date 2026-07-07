package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const (
	projectsPath = "/api/v4/groups/testgroup/projects"
	mrsPath      = "/api/v4/groups/testgroup/merge_requests"
	graphqlPath  = "/api/v4/graphql"
)

func serveFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func readCursor(t *testing.T, r *http.Request) string {
	t.Helper()
	var body struct {
		Variables struct {
			After *string `json:"after"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode graphql request: %v", err)
	}
	if body.Variables.After == nil {
		return ""
	}
	return *body.Variables.After
}

func mustNewProvider(t *testing.T, srv *httptest.Server, opts Options) *Provider {
	t.Helper()
	opts.HTTPClient = srv.Client()
	opts.BaseURL = srv.URL
	opts.GraphQLURL = srv.URL + graphqlPath
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestSnapshotMergesAndPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == projectsPath:
			w.Header().Set("RateLimit-Remaining", "59")
			if r.URL.Query().Get("page") == "2" {
				serveFixture(t, w, "projects_page2.json")
				return
			}
			w.Header().Set("X-Next-Page", "2")
			serveFixture(t, w, "projects_page1.json")
		case r.Method == http.MethodGet && r.URL.Path == mrsPath:
			w.Header().Set("RateLimit-Remaining", "58")
			serveFixture(t, w, "mrs_page1.json")
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			w.Header().Set("RateLimit-Remaining", "1990")
			switch readCursor(t, r) {
			case "":
				serveFixture(t, w, "vulns_page1.json")
			case "CUR2":
				serveFixture(t, w, "vulns_page2.json")
			default:
				t.Errorf("unexpected graphql cursor")
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{}).Snapshot(context.Background(), "testgroup")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	want := provider.Snapshot{
		Repos: []provider.RepoMetrics{
			{Name: "testgroup/alpha", OpenReviewItems: 3, Findings: []provider.Finding{
				{Severity: "high", Category: "dependency"},
				{Severity: "critical", Category: "static_analysis"},
			}},
			{Name: "testgroup/beta", OpenReviewItems: 0},
			{Name: "testgroup/delta", OpenReviewItems: 0, Findings: []provider.Finding{
				{Severity: "medium", Category: "secret"}, // GraphQL-only project (not in the projects list)
			}},
			{Name: "testgroup/gamma", OpenReviewItems: 1, Findings: []provider.Finding{
				{Severity: "low", Category: "container"}, // the DAST finding on alpha is skipped (unmapped)
			}},
		},
		RateLimits: []provider.RateLimit{
			{Resource: "rest", Remaining: 58},
			{Resource: "graphql", Remaining: 1990},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("snapshot mismatch (-want +got):\n%s", diff)
	}
}

func TestSnapshotVulnUnavailableIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case projectsPath:
			serveFixture(t, w, "projects_page1.json") // single page (no X-Next-Page)
		case mrsPath:
			serveFixture(t, w, "mrs_page1.json")
		case graphqlPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"group":{"vulnerabilities":null}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{}).Snapshot(context.Background(), "testgroup")
	if err != nil {
		t.Fatalf("Snapshot returned a hard error, want partial: %v", err)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("repos = %+v, want the 2 REST projects (findings unavailable)", got.Repos)
	}
	for _, r := range got.Repos {
		if len(r.Findings) != 0 {
			t.Errorf("repo %s has findings %+v, want none (vuln source unavailable)", r.Name, r.Findings)
		}
	}
	if !slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL}) {
		t.Errorf("SourceErrors = %+v, want graphql", got.SourceErrors)
	}
	if slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceREST}) {
		t.Errorf("SourceErrors = %+v, must not contain rest (it succeeded)", got.SourceErrors)
	}
}

func TestSnapshotProjectsErrorIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case projectsPath:
			w.WriteHeader(http.StatusInternalServerError)
		case graphqlPath:
			w.Header().Set("RateLimit-Remaining", "1990")
			switch readCursor(t, r) {
			case "":
				serveFixture(t, w, "vulns_page1.json")
			default:
				serveFixture(t, w, "vulns_page2.json")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{}).Snapshot(context.Background(), "testgroup")
	if err != nil {
		t.Fatalf("Snapshot returned a hard error, want partial: %v", err)
	}
	if len(got.Repos) == 0 {
		t.Fatal("repos empty, want GraphQL-derived repos when REST fails")
	}
	if !slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceREST}) {
		t.Errorf("SourceErrors = %+v, want rest", got.SourceErrors)
	}
	if slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL}) {
		t.Errorf("SourceErrors = %+v, must not contain graphql (it succeeded)", got.SourceErrors)
	}
}

func TestNewAuthSelection(t *testing.T) {
	if _, err := New(Options{Token: "glpat-x"}); err != nil {
		t.Fatalf("token auth: %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New with no credentials: got nil error, want failure")
	}
}

func FuzzMapVulnerabilities(f *testing.F) {
	f.Add([]byte(`{"data":{"group":{"vulnerabilities":{"nodes":[{"severity":"HIGH","reportType":"SAST","project":{"fullPath":"g/p"}}]}}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"data":{"group":null}}`))
	f.Add([]byte(`{"data":{"group":{"vulnerabilities":null}}}`))
	f.Add([]byte(`{"data":{"group":{"vulnerabilities":{"nodes":null}}}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var gr vulnResponse
		if err := json.Unmarshal(data, &gr); err != nil {
			return
		}
		mapVulnerabilities(&gr, map[string][]provider.Finding{}) // must not panic
	})
}
