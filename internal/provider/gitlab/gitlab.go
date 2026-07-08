// Package gitlab implements provider.Provider for GitLab. It collects open merge
// request counts per project via the REST API (a group projects list plus one
// group-wide MR sweep) and open security-scan findings via a single group-level
// GraphQL query. It takes no dependency on a GitLab SDK; both sources are hand-rolled
// over net/http, mirroring the GitHub provider.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/failsafe-go/failsafe-go/failsafehttp"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const (
	defaultBaseURL  = "https://gitlab.com"
	apiV4           = "/api/v4"
	defaultMaxPages = 100

	// Target types. A group uses group-scoped project/MR/vulnerability endpoints; a
	// user uses /users/{id}/projects with per-project MR counts and has no
	// vulnerabilities API (Ultimate/group-only), so findings are skipped for users.
	targetGroup = "group"
	targetUser  = "user"
)

// Options configures the GitLab provider. Token is required. BaseURL points at a
// self-hosted instance root (for example https://gitlab.example.com); "/api/v4" is
// appended. Bearer selects "Authorization: Bearer" (OAuth2 tokens) over the default
// "PRIVATE-TOKEN" header. GraphQLURL and HTTPClient exist to point tests at an
// httptest server; when HTTPClient is set it is used verbatim, bypassing the built-in
// auth and retry transport.
type Options struct {
	Token           string
	TargetType      string // "group" (default) or "user"
	BaseURL         string
	GraphQLURL      string
	Bearer          bool
	IncludeArchived bool
	HTTPClient      *http.Client
}

// Provider polls a GitLab group or user for review items and security findings.
type Provider struct {
	rest       *restClient
	graphql    *graphqlClient
	targetType string
	maxPages   int
}

var _ provider.Provider = (*Provider)(nil)

// New builds a GitLab provider, selecting token auth and wiring a Retry-After-aware
// retry/backoff transport.
func New(opts Options) (*Provider, error) {
	httpClient, err := buildHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	base := opts.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	apiBase := ensureAPIBase(base)
	gqlEndpoint := opts.GraphQLURL
	if gqlEndpoint == "" {
		gqlEndpoint = apiBase + "/graphql"
	}
	targetType := opts.TargetType
	if targetType == "" {
		targetType = targetGroup
	}
	return &Provider{
		rest:       &restClient{httpClient: httpClient, apiBase: apiBase, includeArchived: opts.IncludeArchived},
		graphql:    &graphqlClient{httpClient: httpClient, endpoint: gqlEndpoint},
		targetType: targetType,
		maxPages:   defaultMaxPages,
	}, nil
}

// Name identifies the provider on the "provider" metric attribute.
func (p *Provider) Name() string { return "gitlab" }

// Snapshot polls the target (group or user). For a group it merges the REST result
// (projects + open MR counts) with the GraphQL findings; a single failing source yields a
// partial snapshot and only a dual failure returns an error. For a user it returns MR
// counts only: GitLab has no user-scoped vulnerabilities API (Ultimate/group-only), so the
// findings call is skipped rather than emitting a recurring SourceError or a false zero.
func (p *Provider) Snapshot(ctx context.Context, target string) (provider.Snapshot, error) {
	rest, restErr := p.collectREST(ctx, target)

	if p.targetType == targetUser {
		if restErr != nil {
			return provider.Snapshot{}, fmt.Errorf("gitlab: rest failed for user %q: %w", target, restErr)
		}
		snap := provider.Snapshot{Repos: mergeRepos(rest.projects, nil)}
		if rest.rateKnown {
			snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceREST, Remaining: rest.rate})
		}
		zlog.Debug().Str("provider", "gitlab").Str("target", target).Int("repos", len(snap.Repos)).
			Msg("gitlab user snapshot assembled (MR counts only; findings unsupported for users)")
		return snap, nil
	}

	vuln, vulnErr := p.collectVulnerabilities(ctx, target)

	if restErr != nil && vulnErr != nil {
		return provider.Snapshot{}, fmt.Errorf("gitlab: all sources failed for %q: %w", target, errors.Join(restErr, vulnErr))
	}

	snap := provider.Snapshot{Repos: mergeRepos(rest.projects, vuln.findings)}
	if rest.rateKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceREST, Remaining: rest.rate})
	}
	if vuln.rateKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceGraphQL, Remaining: vuln.rate})
	}
	if restErr != nil {
		zlog.Warn().Err(restErr).Str("provider", "gitlab").Str("source", provider.SourceREST).Str("target", target).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceREST})
	}
	if vulnErr != nil {
		zlog.Warn().Err(vulnErr).Str("provider", "gitlab").Str("source", provider.SourceGraphQL).Str("target", target).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL})
	}
	zlog.Debug().Str("provider", "gitlab").Str("target", target).
		Int("repos", len(snap.Repos)).Int("rate_limits", len(snap.RateLimits)).
		Msg("gitlab snapshot assembled")
	return snap, nil
}

// mergeRepos combines the REST projects (the denominator, with open MR counts) and the
// GraphQL findings (keyed by fullPath) into a deterministic slice sorted by name, each
// repo's findings sorted by category then severity.
//
// Unlike the GitHub provider (which keys on the bare repo name), a GitLab group may
// span subgroups with duplicate project names, so the key and emitted Name are the
// project's path_with_namespace (== the GraphQL fullPath), which is unique.
func mergeRepos(projects []restProject, vulnFindings map[string][]provider.Finding) []provider.RepoMetrics {
	byKey := make(map[string]*provider.RepoMetrics)
	get := func(key string) *provider.RepoMetrics {
		r, ok := byKey[key]
		if !ok {
			r = &provider.RepoMetrics{Name: key}
			byKey[key] = r
		}
		return r
	}
	for _, pr := range projects {
		get(pr.pathWithNamespace).OpenReviewItems = pr.openMRs
	}
	for fullPath, fs := range vulnFindings {
		r := get(fullPath)
		r.Findings = append(r.Findings, fs...)
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	out := make([]provider.RepoMetrics, 0, len(keys))
	for _, k := range keys {
		r := byKey[k]
		sortFindings(r.Findings)
		out = append(out, *r)
	}
	return out
}

func sortFindings(fs []provider.Finding) {
	slices.SortFunc(fs, func(a, b provider.Finding) int {
		if c := strings.Compare(a.Category, b.Category); c != 0 {
			return c
		}
		return strings.Compare(a.Severity, b.Severity)
	})
}

func buildHTTPClient(opts Options) (*http.Client, error) {
	if opts.HTTPClient != nil {
		return opts.HTTPClient, nil
	}
	if opts.Token == "" {
		return nil, fmt.Errorf("gitlab: no credentials: set GITLAB_TOKEN")
	}
	retry := failsafehttp.NewRetryPolicyBuilder().WithBackoff(time.Second, 30*time.Second).WithMaxRetries(3).Build() //nolint:bodyclose // generic type param, not a response
	base := failsafehttp.NewRoundTripper(http.DefaultTransport, retry)
	return &http.Client{Transport: &tokenTransport{token: opts.Token, bearer: opts.Bearer, base: base}}, nil
}

// tokenTransport adds the GitLab auth header without mutating the caller's request.
type tokenTransport struct {
	token  string
	bearer bool
	base   http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if t.bearer {
		r.Header.Set("Authorization", "Bearer "+t.token)
	} else {
		r.Header.Set("PRIVATE-TOKEN", t.token)
	}
	return t.base.RoundTrip(r)
}

func ensureAPIBase(root string) string {
	root = strings.TrimSuffix(root, "/")
	if strings.HasSuffix(root, apiV4) {
		return root
	}
	return root + apiV4
}

// escapeGroup URL-encodes a full-path group id ("grp/sub" -> "grp%2Fsub"); a numeric id
// or a single-segment path passes through unchanged.
func escapeGroup(group string) string { return url.PathEscape(group) }
