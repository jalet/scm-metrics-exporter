package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

func TestDependabotResolution(t *testing.T) {
	cases := map[string]string{
		"":               provider.ResolutionFixed, // state=fixed, no dismiss reason
		"inaccurate":     provider.ResolutionDismissedNotARisk,
		"not_used":       provider.ResolutionDismissedNotARisk,
		"tolerable_risk": provider.ResolutionDismissedAcceptedRisk,
		"no_bandwidth":   provider.ResolutionDismissedAcceptedRisk,
		"fix_started":    provider.ResolutionDismissedAcceptedRisk,
		"unknown_reason": provider.ResolutionDismissedAcceptedRisk, // conservative default
	}
	for reason, want := range cases {
		if got := dependabotResolution("fixed", reason); reason == "" && got != want {
			t.Errorf("state=fixed: got %q want %q", got, want)
		}
	}
	for reason, want := range cases {
		if reason == "" {
			continue
		}
		if got := dependabotResolution("dismissed", reason); got != want {
			t.Errorf("dismissed reason %q: got %q want %q", reason, got, want)
		}
	}
}

func TestSecretScanningResolution(t *testing.T) {
	cases := map[string]string{
		"revoked":         provider.ResolutionFixed,
		"false_positive":  provider.ResolutionDismissedNotARisk,
		"used_in_tests":   provider.ResolutionDismissedNotARisk,
		"pattern_deleted": provider.ResolutionDismissedNotARisk,
		"wont_fix":        provider.ResolutionDismissedAcceptedRisk,
		"":                provider.ResolutionDismissedAcceptedRisk,
	}
	for reason, want := range cases {
		if got := secretScanningResolution(reason); got != want {
			t.Errorf("secret resolution %q: got %q want %q", reason, got, want)
		}
	}
}

func TestCodeScanningResolution(t *testing.T) {
	if got := codeScanningResolution("fixed", ""); got != provider.ResolutionFixed {
		t.Errorf("fixed: got %q", got)
	}
	if got := codeScanningResolution("dismissed", "false positive"); got != provider.ResolutionDismissedNotARisk {
		t.Errorf("false positive: got %q", got)
	}
	if got := codeScanningResolution("dismissed", "used in tests"); got != provider.ResolutionDismissedNotARisk {
		t.Errorf("used in tests: got %q", got)
	}
	if got := codeScanningResolution("dismissed", "won't fix"); got != provider.ResolutionDismissedAcceptedRisk {
		t.Errorf("won't fix: got %q", got)
	}
}

// TestSnapshotRepoLifecycle exercises the SnapshotRepo wiring end to end: lifecycle
// collection is opt-in, and when enabled it must populate ResolvedFindings from the
// resolved code-scanning/secret-scanning/Dependabot sources without disturbing the open
// findings collected on the existing paths.
func TestSnapshotRepoLifecycle(t *testing.T) {
	fixedAt := time.Now().Add(-1 * time.Hour)
	createdAt := fixedAt.Add(-2 * time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(repoGraphQL))
		case r.URL.Path == "/repos/acme/widget/code-scanning/alerts":
			switch r.URL.Query().Get("state") {
			case "fixed":
				_, _ = w.Write([]byte(`[{
					"number": 7,
					"rule": {"security_severity_level": "high"},
					"tool": {"name": "CodeQL"},
					"created_at": "` + createdAt.UTC().Format(time.RFC3339) + `",
					"fixed_at": "` + fixedAt.UTC().Format(time.RFC3339) + `"
				}]`))
			default: // "open" and "dismissed"
				_, _ = w.Write([]byte(`[]`))
			}
		case r.URL.Path == "/repos/acme/widget/secret-scanning/alerts":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/repos/acme/widget/dependabot/alerts":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{CollectLifecycle: true}).SnapshotRepo(context.Background(), "acme", "widget")
	if err != nil {
		t.Fatalf("SnapshotRepo: %v", err)
	}
	if len(got.SourceErrors) != 0 {
		t.Fatalf("SourceErrors = %+v, want none", got.SourceErrors)
	}
	if len(got.Repos) != 1 {
		t.Fatalf("repos = %+v, want 1", got.Repos)
	}

	resolved := got.Repos[0].ResolvedFindings
	if len(resolved) != 1 {
		t.Fatalf("ResolvedFindings = %+v, want exactly one entry", resolved)
	}
	rf := resolved[0]
	if rf.Resolution != provider.ResolutionFixed {
		t.Errorf("Resolution = %q, want %q", rf.Resolution, provider.ResolutionFixed)
	}
	if rf.Category != provider.CategoryStaticAnalysis {
		t.Errorf("Category = %q, want %q", rf.Category, provider.CategoryStaticAnalysis)
	}
	if d := rf.ResolvedAt.Sub(rf.CreatedAt); d != 2*time.Hour {
		t.Errorf("ResolvedAt.Sub(CreatedAt) = %v, want 2h", d)
	}
}
