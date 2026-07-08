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
		name string
		sel  Selector
		want []string
	}{
		{"empty", Selector{}, []string{"acme/svc-a", "acme/web", "acme/legacy"}},
		{"include visibility private", Selector{Include: Filter{Visibility: []string{"private"}}}, []string{"acme/svc-a", "acme/legacy"}},
		{"include topic go", Selector{Include: Filter{Topics: []string{"go"}}}, []string{"acme/svc-a", "acme/legacy"}},
		{"include name glob", Selector{Include: Filter{NamePatterns: []string{"acme/svc-*"}}}, []string{"acme/svc-a"}},
		{"exclude archived", Selector{Exclude: Filter{Archived: ptr.To(true)}}, []string{"acme/svc-a", "acme/web"}},
		{"include go minus archived", Selector{Include: Filter{Topics: []string{"go"}}, Exclude: Filter{Archived: ptr.To(true)}}, []string{"acme/svc-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListGitLabProjects(context.Background(), auth, "acme", "group", tc.sel)
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
	if _, err := ListGitLabProjects(context.Background(), GitLabAuth{}, "acme", "group", Selector{}); err == nil {
		t.Fatal("ListGitLabProjects with no token: got nil error, want failure")
	}
}
