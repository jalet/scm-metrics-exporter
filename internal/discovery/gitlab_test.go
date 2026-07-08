package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"k8s.io/utils/ptr"
)

const groupProjects = `[
	{"path_with_namespace":"acme/svc-a","visibility":"private","archived":false,"topics":["go"]},
	{"path_with_namespace":"acme/web","visibility":"public","archived":false,"topics":["js"]},
	{"path_with_namespace":"acme/legacy","visibility":"private","archived":true,"topics":["go"]}
]`

func TestListGitLabProjectsFilters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/groups/acme/projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Next-Page", "") // single page
		_, _ = w.Write([]byte(groupProjects))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	auth := GitLabAuth{Token: "glpat", HTTPClient: srv.Client(), BaseURL: srv.URL}

	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"no filter", Filter{}, []string{"acme/svc-a", "acme/web", "acme/legacy"}},
		{"visibility private", Filter{Visibility: []string{"private"}}, []string{"acme/svc-a", "acme/legacy"}},
		{"topic go", Filter{Topics: []string{"go"}}, []string{"acme/svc-a", "acme/legacy"}},
		{"name glob", Filter{NamePatterns: []string{"acme/svc-*"}}, []string{"acme/svc-a"}},
		{"non-archived", Filter{Archived: ptr.To(false)}, []string{"acme/svc-a", "acme/web"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListGitLabProjects(context.Background(), auth, "acme", "group", tc.filter)
			if err != nil {
				t.Fatalf("ListGitLabProjects: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ListGitLabProjects = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListGitLabProjectsNoCredentials(t *testing.T) {
	if _, err := ListGitLabProjects(context.Background(), GitLabAuth{}, "acme", "group", Filter{}); err == nil {
		t.Fatal("ListGitLabProjects with no token: got nil error, want failure")
	}
}
