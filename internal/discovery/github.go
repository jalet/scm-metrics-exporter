// Package discovery enumerates a target's repositories for the operator. It is the cheap
// first half of the collection model: the operator lists repositories on an interval and
// dispatches one collection Job per repository. Discovery is deliberately Kubernetes- and
// OpenTelemetry-agnostic so it can be unit-tested against an httptest server.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v89/github"
)

// maxPages bounds pagination so a misbehaving API cannot loop forever.
const maxPages = 100

// GitHubAuth selects how the discovery client authenticates. The App trio takes
// precedence over Token; both are read from the CR's credentials Secret by the caller.
type GitHubAuth struct {
	Token             string
	AppID             int64
	AppInstallationID int64
	AppPrivateKeyPEM  []byte
	// BaseURL overrides the REST endpoint (tests point this at an httptest server; it must
	// accept a trailing slash).
	BaseURL string
	// HTTPClient, when set, is used verbatim (tests inject the httptest client).
	HTTPClient *http.Client
}

// Filter matches repositories by attribute. Criteria are ANDed; an empty filter matches
// everything.
type Filter struct {
	Topics       []string
	Visibility   []string
	NamePatterns []string
	Archived     *bool
}

// isEmpty reports whether the filter has no criteria.
func (f Filter) isEmpty() bool {
	return len(f.Topics) == 0 && len(f.Visibility) == 0 && len(f.NamePatterns) == 0 && f.Archived == nil
}

// Selector chooses repositories: Include picks the candidate set (an empty Include matches
// every repository), then Exclude removes any repository it matches. An empty Exclude
// removes nothing (unlike a filter, which matches everything when empty), so exclusion is
// guarded on the exclude filter being non-empty.
type Selector struct {
	Include Filter
	Exclude Filter
}

// selects reports whether a repository with the given attributes passes the selector.
// matched is the per-provider include/exclude test (matches against that provider's repo).
func (s Selector) selects(matched func(Filter) bool) bool {
	if !matched(s.Include) {
		return false
	}
	if !s.Exclude.isEmpty() && matched(s.Exclude) {
		return false
	}
	return true
}

// NewGitHubClient builds a go-github REST client for discovery from auth.
func NewGitHubClient(auth GitHubAuth) (*gh.Client, error) {
	httpClient, token, err := transport(auth)
	if err != nil {
		return nil, err
	}
	var opts []gh.ClientOptionsFunc
	if httpClient != nil {
		opts = append(opts, gh.WithHTTPClient(httpClient))
	}
	if token != "" {
		opts = append(opts, gh.WithAuthToken(token))
	}
	if auth.BaseURL != "" {
		base := auth.BaseURL
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		opts = append(opts, gh.WithURLs(&base, &base))
	}
	client, err := gh.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("discovery: github client: %w", err)
	}
	return client, nil
}

// transport selects the HTTP client and/or PAT for discovery: an App installation
// transport, a PAT applied via WithAuthToken, or a test-injected HTTPClient.
func transport(auth GitHubAuth) (httpClient *http.Client, token string, err error) {
	if auth.HTTPClient != nil {
		return auth.HTTPClient, auth.Token, nil
	}
	switch {
	case auth.AppID != 0 && auth.AppInstallationID != 0 && len(auth.AppPrivateKeyPEM) > 0:
		itr, e := ghinstallation.New(http.DefaultTransport, auth.AppID, auth.AppInstallationID, auth.AppPrivateKeyPEM)
		if e != nil {
			return nil, "", fmt.Errorf("discovery: app auth: %w", e)
		}
		return &http.Client{Transport: itr}, "", nil
	case auth.Token != "":
		return nil, auth.Token, nil
	default:
		return nil, "", errors.New("discovery: no github credentials")
	}
}

// ListRepos returns the names of the target's repositories that pass the selector.
// targetType is "org" (default) or "user".
func ListRepos(ctx context.Context, client *gh.Client, owner, targetType string, sel Selector) ([]string, error) {
	var out []string
	if targetType == "user" {
		opts := &gh.RepositoryListByUserOptions{Type: "owner"}
		opts.PerPage = 100
		for page := 0; page < maxPages; page++ {
			repos, resp, err := client.Repositories.ListByUser(ctx, owner, opts)
			if err != nil {
				return nil, fmt.Errorf("discovery: list user %q repos: %w", owner, err)
			}
			out = appendMatching(out, repos, sel)
			if resp == nil || resp.NextPage == 0 {
				return out, nil
			}
			opts.Page = resp.NextPage
		}
		return out, nil
	}
	opts := &gh.RepositoryListByOrgOptions{}
	opts.PerPage = 100
	for page := 0; page < maxPages; page++ {
		repos, resp, err := client.Repositories.ListByOrg(ctx, owner, opts)
		if err != nil {
			return nil, fmt.Errorf("discovery: list org %q repos: %w", owner, err)
		}
		out = appendMatching(out, repos, sel)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func appendMatching(out []string, repos []*gh.Repository, sel Selector) []string {
	for _, r := range repos {
		if r.GetName() != "" && sel.selects(func(f Filter) bool { return matches(r, f) }) {
			out = append(out, r.GetName())
		}
	}
	return out
}

func matches(r *gh.Repository, f Filter) bool {
	if f.Archived != nil && r.GetArchived() != *f.Archived {
		return false
	}
	if len(f.Visibility) > 0 && !containsFold(f.Visibility, r.GetVisibility()) {
		return false
	}
	if len(f.Topics) > 0 && !anyShared(r.Topics, f.Topics) {
		return false
	}
	if len(f.NamePatterns) > 0 && !anyGlob(f.NamePatterns, r.GetName()) {
		return false
	}
	return true
}

func containsFold(set []string, v string) bool {
	for _, s := range set {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func anyShared(have, want []string) bool {
	for _, w := range want {
		if containsFold(have, w) {
			return true
		}
	}
	return false
}

func anyGlob(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}
