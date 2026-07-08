package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"k8s.io/utils/ptr"
)

const orgRepos = `[
	{"name":"svc-api","visibility":"private","archived":false,"topics":["go","team-a"]},
	{"name":"svc-web","visibility":"public","archived":false,"topics":["js"]},
	{"name":"legacy","visibility":"private","archived":true,"topics":["go"]}
]`

func TestListReposFilters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(orgRepos))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewGitHubClient(GitHubAuth{Token: "x", HTTPClient: srv.Client(), BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewGitHubClient: %v", err)
	}

	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"no filter matches all", Filter{}, []string{"svc-api", "svc-web", "legacy"}},
		{"visibility private", Filter{Visibility: []string{"private"}}, []string{"svc-api", "legacy"}},
		{"topic go", Filter{Topics: []string{"go"}}, []string{"svc-api", "legacy"}},
		{"name glob svc-*", Filter{NamePatterns: []string{"svc-*"}}, []string{"svc-api", "svc-web"}},
		{"archived only", Filter{Archived: ptr.To(true)}, []string{"legacy"}},
		{"non-archived", Filter{Archived: ptr.To(false)}, []string{"svc-api", "svc-web"}},
		{"ANDed criteria", Filter{Visibility: []string{"private"}, Archived: ptr.To(false)}, []string{"svc-api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListRepos(context.Background(), client, "acme", "org", tc.filter)
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ListRepos = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewGitHubClientNoCredentials(t *testing.T) {
	if _, err := NewGitHubClient(GitHubAuth{}); err == nil {
		t.Fatal("NewGitHubClient with no credentials: got nil error, want failure")
	}
}
