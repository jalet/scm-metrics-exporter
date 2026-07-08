package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// readGraphQL decodes a GraphQL request body once, returning the query text (used to
// route between the vulnerabilities and projects-posture queries, which share the
// endpoint) and the pagination cursor.
func readGraphQL(t *testing.T, r *http.Request) (query, after string) {
	t.Helper()
	var body struct {
		Query     string `json:"query"`
		Variables struct {
			After *string `json:"after"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode graphql request: %v", err)
	}
	if body.Variables.After != nil {
		after = *body.Variables.After
	}
	return body.Query, after
}

// isPostureQuery reports whether a GraphQL query is the projects-posture query rather
// than the vulnerabilities query.
func isPostureQuery(query string) bool { return strings.Contains(query, "projects(") }

// emptyPostureBody is a valid projects-posture response with no projects: it lets a
// handler satisfy the posture call without adding repos or a source error.
const emptyPostureBody = `{"data":{"group":{"projects":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`

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
			query, cursor := readGraphQL(t, r)
			if isPostureQuery(query) {
				serveFixture(t, w, "projects_posture_page1.json") // no RateLimit-Remaining: graphql rate stays the vulns reading
				return
			}
			w.Header().Set("RateLimit-Remaining", "1990")
			switch cursor {
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
			{Name: "testgroup/alpha", OpenReviewItems: 3, Posture: &provider.RepoPosture{Visibility: "private", DependabotEnabled: true, BranchProtected: true}, Findings: []provider.Finding{
				{Severity: "high", Category: "dependency"},
				{Severity: "critical", Category: "static_analysis"},
			}},
			{Name: "testgroup/beta", OpenReviewItems: 0, Posture: &provider.RepoPosture{Visibility: "internal"}},
			{Name: "testgroup/delta", OpenReviewItems: 0, Findings: []provider.Finding{
				{Severity: "medium", Category: "secret"}, // GraphQL-vuln-only project (not in the projects list, so no posture)
			}},
			{Name: "testgroup/gamma", OpenReviewItems: 1, Posture: &provider.RepoPosture{Visibility: "public", Archived: true}, Findings: []provider.Finding{
				{Severity: "low", Category: "container"}, // the DAST finding on alpha is skipped (unmapped); protected non-default rule -> not branch_protected
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
			if query, _ := readGraphQL(t, r); isPostureQuery(query) {
				_, _ = w.Write([]byte(emptyPostureBody)) // posture succeeds; only vulnerabilities is unavailable
				return
			}
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
			query, cursor := readGraphQL(t, r)
			if isPostureQuery(query) {
				_, _ = w.Write([]byte(emptyPostureBody)) // posture succeeds; only REST projects fails
				return
			}
			w.Header().Set("RateLimit-Remaining", "1990")
			switch cursor {
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

func TestSnapshotUserTarget(t *testing.T) {
	graphqlCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/users/alice/projects":
			w.Header().Set("RateLimit-Remaining", "59")
			_, _ = w.Write([]byte(`[{"id":10,"path_with_namespace":"alice/proj"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/10/merge_requests":
			w.Header().Set("RateLimit-Remaining", "58")
			_, _ = w.Write([]byte(`[{},{}]`)) // two open MRs
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			graphqlCalled = true // GitLab has no user-scoped vulnerabilities API; must be skipped
			t.Error("graphql must not be called for a user target")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{TargetType: "user"}).Snapshot(context.Background(), "alice")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if graphqlCalled {
		t.Fatal("graphql was queried for a user target")
	}
	if len(got.SourceErrors) != 0 {
		t.Fatalf("SourceErrors = %+v, want none for a user target", got.SourceErrors)
	}
	want := []provider.RepoMetrics{{Name: "alice/proj", OpenReviewItems: 2}}
	if diff := cmp.Diff(want, got.Repos); diff != "" {
		t.Errorf("repos mismatch (-want +got):\n%s", diff)
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

func TestMapProjectPosture(t *testing.T) {
	body := `{"data":{"group":{"projects":{"nodes":[
		{"fullPath":"g/protected","visibility":"PRIVATE","archived":false,
		 "securityScanners":{"enabled":["SAST","DEPENDENCY_SCANNING"]},
		 "branchRules":{"nodes":[{"isDefault":true,"isProtected":true}]}},
		{"fullPath":"g/loose","visibility":"public","archived":true,
		 "securityScanners":{"enabled":["SAST"]},
		 "branchRules":{"nodes":[{"isDefault":false,"isProtected":true},{"isDefault":true,"isProtected":false}]}},
		{"fullPath":"g/null","visibility":"internal","securityScanners":null,"branchRules":null}
	]}}}}`
	var gr projectsResponse
	if err := json.Unmarshal([]byte(body), &gr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]*provider.RepoPosture{}
	mapProjectPosture(&gr, got)

	want := map[string]*provider.RepoPosture{
		"g/protected": {Visibility: "private", Archived: false, DependabotEnabled: true, BranchProtected: true},
		// dependency scanning off; default-branch rule is not protected, protected rule is not default -> not protected.
		"g/loose": {Visibility: "public", Archived: true, DependabotEnabled: false, BranchProtected: false},
		// null securityScanners/branchRules must not panic and read as false.
		"g/null": {Visibility: "internal"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("posture mismatch (-want +got):\n%s", diff)
	}
}

func FuzzMapProjectPosture(f *testing.F) {
	f.Add([]byte(`{"data":{"group":{"projects":{"nodes":[{"fullPath":"g/p","visibility":"private","securityScanners":{"enabled":["DEPENDENCY_SCANNING"]},"branchRules":{"nodes":[{"isDefault":true,"isProtected":true}]}}]}}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"data":{"group":null}}`))
	f.Add([]byte(`{"data":{"group":{"projects":null}}}`))
	f.Add([]byte(`{"data":{"group":{"projects":{"nodes":null}}}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var gr projectsResponse
		if err := json.Unmarshal(data, &gr); err != nil {
			return
		}
		mapProjectPosture(&gr, map[string]*provider.RepoPosture{}) // must not panic
	})
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
