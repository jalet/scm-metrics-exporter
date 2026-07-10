package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	gh "github.com/google/go-github/v89/github"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

func TestIsRateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"primary rate limit", &gh.RateLimitError{Message: "quota exhausted"}, true},
		{"secondary abuse limit", &gh.AbuseRateLimitError{Message: "secondary rate limit"}, true},
		{"plain 403 not accessible", &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusForbidden}}, false},
		{"nil error", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRateLimit(tc.err); got != tc.want {
				t.Errorf("isRateLimit(%v) = %v, want %v", tc.err, got, tc.want)
			}
			// A rate-limit error must never be classified as an accessible-skip.
			if tc.want && notAccessible(tc.err) {
				t.Errorf("notAccessible(%v) = true, want false for a rate-limit error", tc.err)
			}
		})
	}
}

// TestSnapshotRepoRateLimitSurfaces verifies that a per-repo code-scanning rate-limit 403
// (GitHub signals primary exhaustion as a 403 with X-RateLimit-Remaining: 0) is reported as
// a REST source error rather than swallowed as "feature not accessible" (empty findings).
func TestSnapshotRepoRateLimitSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(repoGraphQL))
		case r.URL.Path == "/repos/acme/widget/code-scanning/alerts":
			w.Header().Set("X-RateLimit-Remaining", "0") // primary rate-limit exhaustion
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		case r.URL.Path == "/repos/acme/widget/secret-scanning/alerts":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/repos/acme/widget":
			_, _ = w.Write([]byte(`{}`)) // secret-scanning posture read
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := mustNewProvider(t, srv, Options{}).SnapshotRepo(context.Background(), "acme", "widget")
	if err != nil {
		t.Fatalf("SnapshotRepo returned a hard error, want partial: %v", err)
	}
	if !slices.Contains(got.SourceErrors, provider.SourceError{Source: provider.SourceREST}) {
		t.Errorf("SourceErrors = %+v, want rest (rate-limit 403 must surface, not be swallowed)", got.SourceErrors)
	}
	if len(got.Repos) != 1 || got.Repos[0].Name != "widget" {
		t.Fatalf("repos = %+v, want widget present (graphql succeeded)", got.Repos)
	}
}
