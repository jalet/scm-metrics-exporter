package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
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
			Cursor *string `json:"cursor"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode graphql request: %v", err)
	}
	if body.Variables.Cursor == nil {
		return ""
	}
	return *body.Variables.Cursor
}

func mustNewProvider(t *testing.T, srv *httptest.Server, opts Options) *Provider {
	t.Helper()
	opts.HTTPClient = srv.Client()
	opts.BaseURL = srv.URL
	opts.GraphQLURL = srv.URL + "/graphql"
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

const codeScanningPath = "/orgs/testorg/code-scanning/alerts"

func TestSnapshotMergesAndPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			switch cursor := readCursor(t, r); cursor {
			case "":
				serveFixture(t, w, "graphql_page1.json")
			case "CURSOR2":
				serveFixture(t, w, "graphql_page2.json")
			default:
				t.Errorf("unexpected graphql cursor %q", cursor)
			}
		case r.Method == http.MethodGet && r.URL.Path == codeScanningPath:
			if r.URL.Query().Get("page") == "2" {
				w.Header().Set("X-RateLimit-Remaining", "4898")
				serveFixture(t, w, "codescan_page2.json")
				return
			}
			w.Header().Set("X-RateLimit-Remaining", "4899")
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, codeScanningPath))
			serveFixture(t, w, "codescan_page1.json")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := mustNewProvider(t, srv, Options{})
	got, err := p.Snapshot(context.Background(), "testorg")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	want := provider.Snapshot{
		Repos: []provider.RepoMetrics{
			{Name: "alpha", OpenReviewItems: 3, Findings: []provider.Finding{
				{Severity: "high", Category: "dependency"},
				{Severity: "medium", Category: "dependency"}, // MODERATE normalized
				{Severity: "high", Category: "static_analysis"},
			}},
			{Name: "beta", OpenReviewItems: 0, Findings: []provider.Finding{
				{Severity: "medium", Category: "static_analysis"},
			}},
			{Name: "gamma", OpenReviewItems: 7, Findings: []provider.Finding{
				{Severity: "critical", Category: "dependency"},
				{Severity: "critical", Category: "static_analysis"},
			}},
		},
		RateLimits: []provider.RateLimit{
			{Resource: "graphql", Remaining: 4989},
			{Resource: "rest", Remaining: 4898},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("snapshot mismatch (-want +got):\n%s", diff)
	}
}

func TestSnapshotCodeScanning403IsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			serveFixture(t, w, "graphql_single.json")
		case r.Method == http.MethodGet && r.URL.Path == codeScanningPath:
			w.Header().Set("X-RateLimit-Remaining", "4900")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := mustNewProvider(t, srv, Options{})
	got, err := p.Snapshot(context.Background(), "testorg")
	if err != nil {
		t.Fatalf("Snapshot returned a hard error, want partial: %v", err)
	}

	if len(got.Repos) != 1 || got.Repos[0].Name != "solo" {
		t.Fatalf("repos = %+v, want single repo solo (graphql succeeded)", got.Repos)
	}
	if len(got.Repos[0].Findings) != 1 || got.Repos[0].Findings[0].Category != provider.CategoryDependency {
		t.Errorf("solo findings = %+v, want only the dependency finding (code scanning failed)", got.Repos[0].Findings)
	}
	if !slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceREST}) {
		t.Errorf("SourceErrors = %+v, want it to contain rest", got.SourceErrors)
	}
	if slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL}) {
		t.Errorf("SourceErrors = %+v, must not contain graphql (it succeeded)", got.SourceErrors)
	}
}

func TestSnapshotCodeScanningToolFilter(t *testing.T) {
	var gotTool string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			serveFixture(t, w, "graphql_single.json")
		case r.Method == http.MethodGet && r.URL.Path == codeScanningPath:
			gotTool = r.URL.Query().Get("tool_name")
			w.Header().Set("X-RateLimit-Remaining", "5000")
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := mustNewProvider(t, srv, Options{CodeScanningTool: "CodeQL"})
	if _, err := p.Snapshot(context.Background(), "testorg"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if gotTool != "CodeQL" {
		t.Errorf("code scanning tool_name = %q, want CodeQL", gotTool)
	}
}

func TestNewAuthSelection(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		if _, err := New(Options{Token: "pat"}); err != nil {
			t.Fatalf("token auth: %v", err)
		}
	})
	t.Run("app", func(t *testing.T) {
		if _, err := New(Options{AppID: 1, AppInstallationID: 2, AppPrivateKeyPath: writeTestKey(t)}); err != nil {
			t.Fatalf("app auth: %v", err)
		}
	})
	t.Run("none", func(t *testing.T) {
		if _, err := New(Options{}); err == nil {
			t.Fatal("New with no credentials: got nil error, want failure")
		}
	})
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func FuzzMapGraphQL(f *testing.F) {
	f.Add([]byte(`{"data":{"organization":{"repositories":{"nodes":[{"name":"a","pullRequests":{"totalCount":1},"vulnerabilityAlerts":{"nodes":[{"securityVulnerability":{"severity":"MODERATE"}}]}}]}},"rateLimit":{"remaining":10}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"data":{"organization":{"repositories":{"nodes":null}}}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var gr graphqlResponse
		if err := json.Unmarshal(data, &gr); err != nil {
			return
		}
		_ = mapGraphQLRepos(&gr) // must not panic on any decoded input
	})
}
