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
		name string
		sel  Selector
		want []string
	}{
		{"empty selector matches all", Selector{}, []string{"svc-api", "svc-web", "legacy"}},
		{"include visibility private", Selector{Include: Filter{Visibility: []string{"private"}}}, []string{"svc-api", "legacy"}},
		{"include topic go", Selector{Include: Filter{Topics: []string{"go"}}}, []string{"svc-api", "legacy"}},
		{"include name glob svc-*", Selector{Include: Filter{NamePatterns: []string{"svc-*"}}}, []string{"svc-api", "svc-web"}},
		{"include archived only", Selector{Include: Filter{Archived: ptr.To(true)}}, []string{"legacy"}},
		{"include ANDed", Selector{Include: Filter{Visibility: []string{"private"}, Archived: ptr.To(false)}}, []string{"svc-api"}},
		{"exclude archived", Selector{Exclude: Filter{Archived: ptr.To(true)}}, []string{"svc-api", "svc-web"}},
		{"exclude topic js", Selector{Exclude: Filter{Topics: []string{"js"}}}, []string{"svc-api", "legacy"}},
		{"include topic go minus archived", Selector{Include: Filter{Topics: []string{"go"}}, Exclude: Filter{Archived: ptr.To(true)}}, []string{"svc-api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListRepos(context.Background(), client, "acme", "org", tc.sel)
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
