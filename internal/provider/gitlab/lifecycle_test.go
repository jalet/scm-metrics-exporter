package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

func TestGitLabResolution(t *testing.T) {
	if got := gitlabResolution("RESOLVED", ""); got != provider.ResolutionFixed {
		t.Errorf("resolved: got %q", got)
	}
	notARisk := []string{"false_positive", "used_in_tests", "not_applicable"}
	for _, r := range notARisk {
		if got := gitlabResolution("DISMISSED", r); got != provider.ResolutionDismissedNotARisk {
			t.Errorf("dismissed %q: got %q want not_a_risk", r, got)
		}
	}
	accepted := []string{"acceptable_risk", "mitigating_control", "anything_else"}
	for _, r := range accepted {
		if got := gitlabResolution("DISMISSED", r); got != provider.ResolutionDismissedAcceptedRisk {
			t.Errorf("dismissed %q: got %q want accepted_risk", r, got)
		}
	}
}

// TestSnapshotRepoResolvedFindings exercises collectResolvedFindings through SnapshotRepo:
// the GraphQL fixture returns two resolved nodes, newest first, one inside the
// resolution window and one well outside it. Only the in-window node must survive.
func TestSnapshotRepoResolvedFindings(t *testing.T) {
	now := time.Now().UTC()
	resolutionWindow := 24 * time.Hour
	resolvedRecent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	detectedRecent := now.Add(-2 * time.Hour).Format(time.RFC3339)
	resolvedOld := now.Add(-48 * time.Hour).Format(time.RFC3339)
	detectedOld := now.Add(-72 * time.Hour).Format(time.RFC3339)

	resolvedVulnsJSON := `{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"id":"gid://gitlab/Vulnerability/1","severity":"HIGH","reportType":"DEPENDENCY_SCANNING","state":"RESOLVED","detectedAt":"` + detectedRecent + `","resolvedAt":"` + resolvedRecent + `","dismissedAt":null,"dismissalReason":null},
		{"id":"gid://gitlab/Vulnerability/2","severity":"LOW","reportType":"SAST","state":"RESOLVED","detectedAt":"` + detectedOld + `","resolvedAt":"` + resolvedOld + `","dismissedAt":null,"dismissalReason":null}
	]}}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			w.Header().Set("Content-Type", "application/json")
			query, _ := readGraphQL(t, r)
			switch {
			case strings.Contains(query, "ProjectResolvedVulns"):
				_, _ = w.Write([]byte(resolvedVulnsJSON))
			case strings.Contains(query, "ProjectPosture"):
				_, _ = w.Write([]byte(projectPostureJSON))
			default: // ProjectVulns (open findings)
				_, _ = w.Write([]byte(`{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`))
			}
		case strings.Contains(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{CollectLifecycle: true, ResolutionWindow: resolutionWindow}).
		SnapshotRepo(context.Background(), "acme", "acme/svc")
	if err != nil {
		t.Fatalf("SnapshotRepo: %v", err)
	}
	if len(got.Repos) != 1 {
		t.Fatalf("repos = %+v, want exactly one", got.Repos)
	}
	resolved := got.Repos[0].ResolvedFindings
	if len(resolved) != 1 {
		t.Fatalf("ResolvedFindings = %+v, want exactly one (the older node is outside the window)", resolved)
	}
	want := provider.ResolvedFinding{
		ID:         "gid://gitlab/Vulnerability/1",
		Category:   provider.CategoryDependency,
		Severity:   "high",
		State:      provider.StateResolved,
		Resolution: provider.ResolutionFixed,
		CreatedAt:  parseGitLabTime(detectedRecent),
		ResolvedAt: parseGitLabTime(resolvedRecent),
	}
	if diff := cmp.Diff(want, resolved[0]); diff != "" {
		t.Errorf("resolved finding mismatch (-want +got):\n%s", diff)
	}
	for _, se := range got.SourceErrors {
		if se.Source == provider.SourceLifecycle {
			t.Errorf("SourceErrors = %+v, want no lifecycle error", got.SourceErrors)
		}
	}
}

// TestCollectResolvedFindingsCrossPageWindow proves that an out-of-window node on an
// earlier page cannot stop paging before an in-window node on a later page is reached.
// The vulnerabilities connection is sorted by detection time, not resolution time, so a
// node detected long ago but resolved recently can sort onto a later page than a node
// that is genuinely outside the resolution window. Page 1 serves only an out-of-window
// node (which used to trigger an early break); page 2, reachable only via its cursor,
// serves the in-window node. Under the old early-break logic this test fails because
// page 2 is never requested and the in-window node is silently dropped.
func TestCollectResolvedFindingsCrossPageWindow(t *testing.T) {
	now := time.Now().UTC()
	resolutionWindow := 24 * time.Hour

	// Page 1: a node resolved well outside the window. Detected recently, resolved long
	// ago, so it sorts first under detected_desc despite being out of window.
	outsideDetected := now.Add(-1 * time.Hour).Format(time.RFC3339)
	outsideResolved := now.Add(-48 * time.Hour).Format(time.RFC3339)
	page1JSON := `{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":true,"endCursor":"CUR1"},"nodes":[
		{"id":"gid://gitlab/Vulnerability/1","severity":"HIGH","reportType":"DEPENDENCY_SCANNING","state":"RESOLVED","detectedAt":"` + outsideDetected + `","resolvedAt":"` + outsideResolved + `","dismissedAt":null,"dismissalReason":null}
	]}}}}`

	// Page 2: a node resolved inside the window. Detected long ago, resolved recently,
	// so it sorts onto a later page even though it is genuinely in-window.
	insideDetected := now.Add(-72 * time.Hour).Format(time.RFC3339)
	insideResolved := now.Add(-1 * time.Hour).Format(time.RFC3339)
	page2JSON := `{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":false},"nodes":[
		{"id":"gid://gitlab/Vulnerability/2","severity":"LOW","reportType":"SAST","state":"RESOLVED","detectedAt":"` + insideDetected + `","resolvedAt":"` + insideResolved + `","dismissedAt":null,"dismissalReason":null}
	]}}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == graphqlPath:
			w.Header().Set("Content-Type", "application/json")
			query, after := readGraphQL(t, r)
			switch {
			case strings.Contains(query, "ProjectResolvedVulns") && after == "":
				_, _ = w.Write([]byte(page1JSON))
			case strings.Contains(query, "ProjectResolvedVulns") && after == "CUR1":
				_, _ = w.Write([]byte(page2JSON))
			case strings.Contains(query, "ProjectPosture"):
				_, _ = w.Write([]byte(projectPostureJSON))
			default: // ProjectVulns (open findings)
				_, _ = w.Write([]byte(`{"data":{"project":{"vulnerabilities":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`))
			}
		case strings.Contains(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{CollectLifecycle: true, ResolutionWindow: resolutionWindow}).
		SnapshotRepo(context.Background(), "acme", "acme/svc")
	if err != nil {
		t.Fatalf("SnapshotRepo: %v", err)
	}
	if len(got.Repos) != 1 {
		t.Fatalf("repos = %+v, want exactly one", got.Repos)
	}
	resolved := got.Repos[0].ResolvedFindings
	if len(resolved) != 1 {
		t.Fatalf("ResolvedFindings = %+v, want exactly one (the page-2 in-window node)", resolved)
	}
	want := provider.ResolvedFinding{
		ID:         "gid://gitlab/Vulnerability/2",
		Category:   provider.CategoryStaticAnalysis,
		Severity:   "low",
		State:      provider.StateResolved,
		Resolution: provider.ResolutionFixed,
		CreatedAt:  parseGitLabTime(insideDetected),
		ResolvedAt: parseGitLabTime(insideResolved),
	}
	if diff := cmp.Diff(want, resolved[0]); diff != "" {
		t.Errorf("resolved finding mismatch (-want +got):\n%s", diff)
	}
}
