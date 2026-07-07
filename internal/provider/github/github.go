// Package github implements the provider.Provider abstraction for GitHub. It
// batches open pull-request counts and Dependabot alerts through one GraphQL query
// per page of repositories, and fetches code scanning alerts through the REST API.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/failsafe-go/failsafe-go/failsafehttp"
	gh "github.com/google/go-github/v89/github"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const (
	defaultGraphQLEndpoint = "https://api.github.com/graphql"
	// defaultMaxPages bounds pagination so a misbehaving API cannot loop forever.
	defaultMaxPages = 100
)

// Options configures the GitHub provider. Auth is selected by which fields are set:
// the App trio (AppID + AppInstallationID + AppPrivateKeyPath) takes precedence,
// otherwise Token (PAT), otherwise New returns an error. BaseURL, GraphQLURL, and
// HTTPClient exist to point tests at an httptest server; when HTTPClient is set it
// is used verbatim, bypassing the built-in auth and retry transport.
type Options struct {
	Token             string
	AppID             int64
	AppInstallationID int64
	AppPrivateKeyPath string
	CodeScanningTool  string // SARIF tool filter; empty counts all tools
	BaseURL           string // REST base URL override (must accept a trailing slash)
	GraphQLURL        string // GraphQL endpoint override
	HTTPClient        *http.Client
}

// Provider polls a GitHub organization for review items and security findings.
type Provider struct {
	rest     *gh.Client
	graphql  *graphqlClient
	toolName string
	maxPages int
}

var _ provider.Provider = (*Provider)(nil)

// New builds a GitHub provider from opts, selecting the authentication method and
// wiring a retry/backoff transport that honors Retry-After.
func New(opts Options) (*Provider, error) {
	httpClient, err := buildHTTPClient(opts)
	if err != nil {
		return nil, err
	}

	restOpts := []gh.ClientOptionsFunc{gh.WithHTTPClient(httpClient)}
	if opts.BaseURL != "" {
		base := ensureTrailingSlash(opts.BaseURL)
		restOpts = append(restOpts, gh.WithURLs(&base, &base))
	}
	rest, err := gh.NewClient(restOpts...)
	if err != nil {
		return nil, fmt.Errorf("github: rest client: %w", err)
	}

	endpoint := opts.GraphQLURL
	if endpoint == "" {
		endpoint = defaultGraphQLEndpoint
	}

	return &Provider{
		rest:     rest,
		graphql:  &graphqlClient{httpClient: httpClient, endpoint: endpoint},
		toolName: opts.CodeScanningTool,
		maxPages: defaultMaxPages,
	}, nil
}

// Name identifies the provider on the "provider" metric attribute.
func (p *Provider) Name() string { return "github" }

// Snapshot polls the organization's repositories. It merges the GraphQL result
// (open PRs + Dependabot findings) with the REST code scanning result. A single
// failing source is recorded in SourceErrors and yields a partial snapshot; only
// when both sources fail does Snapshot return an error.
func (p *Provider) Snapshot(ctx context.Context, org string) (provider.Snapshot, error) {
	gql, gqlErr := p.collectGraphQL(ctx, org)
	cs, csErr := p.collectCodeScanning(ctx, org)

	if gqlErr != nil && csErr != nil {
		return provider.Snapshot{}, fmt.Errorf("github: all sources failed for %q: %w", org, errors.Join(gqlErr, csErr))
	}

	snap := provider.Snapshot{Repos: mergeRepos(gql.repos, cs.findings)}
	if gql.rateKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceGraphQL, Remaining: gql.rateRemaining})
	}
	if cs.rateKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceREST, Remaining: cs.rate})
	}
	if gqlErr != nil {
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL})
	}
	if csErr != nil {
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceREST})
	}
	return snap, nil
}

// mergeRepos combines the GraphQL repositories (open PRs + Dependabot findings) with
// the code scanning findings (keyed by repo name) into a deterministic slice, sorted
// by repository name with each repository's findings sorted by category then severity.
func mergeRepos(gqlRepos []graphqlRepo, csFindings map[string][]provider.Finding) []provider.RepoMetrics {
	byName := make(map[string]*provider.RepoMetrics)
	get := func(name string) *provider.RepoMetrics {
		r, ok := byName[name]
		if !ok {
			r = &provider.RepoMetrics{Name: name}
			byName[name] = r
		}
		return r
	}
	for _, gr := range gqlRepos {
		r := get(gr.name)
		r.OpenReviewItems = gr.openPRs
		r.Findings = append(r.Findings, gr.findings...)
	}
	for name, fs := range csFindings {
		get(name).Findings = append(get(name).Findings, fs...)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)

	out := make([]provider.RepoMetrics, 0, len(names))
	for _, name := range names {
		r := byName[name]
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

// buildHTTPClient returns the HTTP client for both the REST and GraphQL calls. Tests
// inject Options.HTTPClient directly; otherwise the client chains an auth transport
// over a Retry-After-aware retry/backoff transport.
func buildHTTPClient(opts Options) (*http.Client, error) {
	if opts.HTTPClient != nil {
		return opts.HTTPClient, nil
	}
	// One line so the single bodyclose false-positive (triggered by the
	// retrypolicy.Builder[*http.Response] type parameter, not a real response body)
	// is suppressed by one directive.
	retry := failsafehttp.NewRetryPolicyBuilder().WithBackoff(time.Second, 30*time.Second).WithMaxRetries(3).Build() //nolint:bodyclose // generic type param, not a response
	base := failsafehttp.NewRoundTripper(http.DefaultTransport, retry)

	authRT, err := authTransport(opts, base)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: authRT}, nil
}

func authTransport(opts Options, base http.RoundTripper) (http.RoundTripper, error) {
	appConfigured := opts.AppID != 0 && opts.AppInstallationID != 0 && opts.AppPrivateKeyPath != ""
	switch {
	case appConfigured:
		itr, err := ghinstallation.NewKeyFromFile(base, opts.AppID, opts.AppInstallationID, opts.AppPrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("github: app auth: %w", err)
		}
		return itr, nil
	case opts.Token != "":
		return &bearerTransport{token: opts.Token, base: base}, nil
	default:
		return nil, fmt.Errorf("github: no credentials: set GITHUB_TOKEN or the GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH trio")
	}
}

// bearerTransport adds a PAT Authorization header without mutating the caller's request.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

func ensureTrailingSlash(u string) string {
	if strings.HasSuffix(u, "/") {
		return u
	}
	return u + "/"
}
