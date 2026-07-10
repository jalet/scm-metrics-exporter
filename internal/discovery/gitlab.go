package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	gitlabDefaultBaseURL = "https://gitlab.com"
	gitlabAPIV4          = "/api/v4"
)

// GitLabAuth selects how the discovery client authenticates. Token is required (GitLab has
// no App-installation model); Bearer chooses the OAuth2 "Authorization: Bearer" header over
// the default "PRIVATE-TOKEN".
type GitLabAuth struct {
	Token      string
	Bearer     bool
	BaseURL    string // instance root (for example https://gitlab.example.com); "/api/v4" is appended
	HTTPClient *http.Client
}

// ListGitLabProjects returns the full paths (path_with_namespace) of the target's projects
// that pass the selector. targetType is "group" (default, includes subgroups) or "user".
// NamePatterns match against the full path (for example "team/*").
func ListGitLabProjects(ctx context.Context, auth GitLabAuth, target, targetType string, sel Selector) ([]string, error) {
	client, err := gitlabHTTPClient(auth)
	if err != nil {
		return nil, err
	}
	apiBase := gitlabEnsureAPIBase(auth.BaseURL)

	var pathTmpl string
	switch targetType {
	case "user":
		pathTmpl = fmt.Sprintf("%s/users/%s/projects?per_page=100", apiBase, url.PathEscape(target))
	default:
		pathTmpl = fmt.Sprintf("%s/groups/%s/projects?include_subgroups=true&per_page=100", apiBase, url.PathEscape(target))
	}

	var out []string
	for page, next := 0, "1"; next != "" && page < maxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pathTmpl+"&page="+next, nil)
		if err != nil {
			return nil, fmt.Errorf("discovery: gitlab request: %w", err)
		}
		resp, err := client.Do(req) //nolint:gosec // apiBase is operator-configured, not attacker input
		if err != nil {
			return nil, fmt.Errorf("discovery: gitlab list projects: %w", err)
		}
		projects, nextPage, err := decodeGitLabProjects(resp)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, pr := range projects {
			if pr.PathWithNamespace != "" && sel.selects(func(f Filter) bool { return matchesGitLab(pr, f) }) {
				out = append(out, pr.PathWithNamespace)
			}
		}
		next = nextPage
	}
	return out, nil
}

type gitlabProject struct {
	PathWithNamespace string   `json:"path_with_namespace"`
	Visibility        string   `json:"visibility"`
	Archived          bool     `json:"archived"`
	Topics            []string `json:"topics"`
}

// decodeGitLabProjects decodes one projects page. The caller owns closing resp.Body.
func decodeGitLabProjects(resp *http.Response) ([]gitlabProject, string, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("discovery: gitlab list projects: http %d", resp.StatusCode)
	}
	var projects []gitlabProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, "", fmt.Errorf("discovery: gitlab decode projects: %w", err)
	}
	return projects, resp.Header.Get("X-Next-Page"), nil
}

func matchesGitLab(pr gitlabProject, f Filter) bool {
	if f.Archived != nil && pr.Archived != *f.Archived {
		return false
	}
	if len(f.Visibility) > 0 && !containsFold(f.Visibility, pr.Visibility) {
		return false
	}
	if len(f.Topics) > 0 && !anyShared(pr.Topics, f.Topics) {
		return false
	}
	if len(f.NamePatterns) > 0 && !anyGlob(f.NamePatterns, pr.PathWithNamespace) {
		return false
	}
	return true
}

// GitLabRateBudget reports the remaining GitLab API budget for auth's token. GitLab has no
// free rate-limit endpoint, so it issues one cheap authenticated GET /version and reads the
// RateLimit-Remaining / RateLimit-Reset (Unix epoch) response headers. An instance with rate
// limiting disabled sends no such headers, yielding Known=false so the caller does not gate.
func GitLabRateBudget(ctx context.Context, auth GitLabAuth) (Budget, error) {
	client, err := gitlabHTTPClient(auth)
	if err != nil {
		return Budget{}, err
	}
	apiBase := gitlabEnsureAPIBase(auth.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/version", nil)
	if err != nil {
		return Budget{}, fmt.Errorf("discovery: gitlab rate limit request: %w", err)
	}
	resp, err := client.Do(req) //nolint:gosec // apiBase is operator-configured, not attacker input
	if err != nil {
		return Budget{}, fmt.Errorf("discovery: gitlab rate limit: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return gitlabBudgetFromHeaders(resp.Header), nil
}

// gitlabBudgetFromHeaders reads a Budget from GitLab's RateLimit-* response headers. Absent or
// unparseable RateLimit-Remaining yields Known=false.
func gitlabBudgetFromHeaders(h http.Header) Budget {
	rem := h.Get("RateLimit-Remaining")
	if rem == "" {
		return Budget{}
	}
	n, err := strconv.Atoi(rem)
	if err != nil {
		return Budget{}
	}
	b := Budget{Remaining: n, Known: true}
	if sec, err := strconv.ParseInt(h.Get("RateLimit-Reset"), 10, 64); err == nil && sec > 0 {
		b.Reset = time.Unix(sec, 0)
	}
	return b
}

func gitlabHTTPClient(auth GitLabAuth) (*http.Client, error) {
	if auth.Token == "" {
		return nil, errors.New("discovery: no gitlab credentials")
	}
	base := http.DefaultTransport
	if auth.HTTPClient != nil && auth.HTTPClient.Transport != nil {
		base = auth.HTTPClient.Transport
	}
	return &http.Client{Transport: &gitlabTokenTransport{token: auth.Token, bearer: auth.Bearer, base: base}}, nil
}

type gitlabTokenTransport struct {
	token  string
	bearer bool
	base   http.RoundTripper
}

func (t *gitlabTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if t.bearer {
		r.Header.Set("Authorization", "Bearer "+t.token)
	} else {
		r.Header.Set("PRIVATE-TOKEN", t.token)
	}
	return t.base.RoundTrip(r)
}

func gitlabEnsureAPIBase(root string) string {
	if root == "" {
		root = gitlabDefaultBaseURL
	}
	root = strings.TrimSuffix(root, "/")
	if strings.HasSuffix(root, gitlabAPIV4) {
		return root
	}
	return root + gitlabAPIV4
}
