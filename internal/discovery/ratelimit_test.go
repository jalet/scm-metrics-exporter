package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubRateBudgetTakesTighterBucket(t *testing.T) {
	const coreReset, graphqlReset = 2000000000, 2000000500
	mux := http.NewServeMux()
	mux.HandleFunc("/rate_limit", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// core is the tighter bucket (10 < 4000), so its remaining and reset must win.
		_, _ = fmt.Fprintf(w, `{"resources":{"core":{"limit":5000,"remaining":10,"reset":%d},"graphql":{"limit":5000,"remaining":4000,"reset":%d}}}`, coreReset, graphqlReset)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewGitHubClient(GitHubAuth{Token: "x", HTTPClient: srv.Client(), BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewGitHubClient: %v", err)
	}

	b, err := GitHubRateBudget(context.Background(), client)
	if err != nil {
		t.Fatalf("GitHubRateBudget: %v", err)
	}
	if !b.Known {
		t.Fatal("budget.Known = false, want true")
	}
	if b.Remaining != 10 {
		t.Errorf("Remaining = %d, want 10 (tighter core bucket)", b.Remaining)
	}
	if want := time.Unix(coreReset, 0); !b.Reset.Equal(want) {
		t.Errorf("Reset = %v, want %v (core bucket's reset)", b.Reset, want)
	}
}

func TestGitLabRateBudgetFromHeaders(t *testing.T) {
	const reset = 2000000000
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit-Remaining", "42")
		w.Header().Set("RateLimit-Reset", fmt.Sprint(reset))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"17.0.0"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b, err := GitLabRateBudget(context.Background(), GitLabAuth{Token: "glpat", HTTPClient: srv.Client(), BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("GitLabRateBudget: %v", err)
	}
	if !b.Known || b.Remaining != 42 {
		t.Errorf("budget = %+v, want Known=true Remaining=42", b)
	}
	if want := time.Unix(reset, 0); !b.Reset.Equal(want) {
		t.Errorf("Reset = %v, want %v", b.Reset, want)
	}
}

func TestGitLabRateBudgetNoHeadersUnknown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"17.0.0"}`)) // no RateLimit-* headers
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b, err := GitLabRateBudget(context.Background(), GitLabAuth{Token: "glpat", HTTPClient: srv.Client(), BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("GitLabRateBudget: %v", err)
	}
	if b.Known {
		t.Errorf("budget.Known = true, want false when the instance sends no RateLimit headers")
	}
}
