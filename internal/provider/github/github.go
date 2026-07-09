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
	ghv88 "github.com/google/go-github/v88/github" // only for ghinstallation.Transport.InstallationTokenOptions (the go-github major ghinstallation/v2 bundles); everything else uses gh (v89)
	gh "github.com/google/go-github/v89/github"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

const (
	defaultGraphQLEndpoint = "https://api.github.com/graphql"
	// defaultMaxPages bounds pagination so a misbehaving API cannot loop forever.
	defaultMaxPages = 100

	// Target types. An org is polled with one org-scoped code-scanning call; a user
	// has no org-scoped code-scanning endpoint, so user targets iterate the user's
	// repositories and call the per-repo endpoint. GraphQL (PRs + Dependabot) uses
	// repositoryOwner(login:) and needs no branch -- it resolves either owner type.
	targetOrg  = "org"
	targetUser = "user"
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
	TargetType        string // "org" (default) or "user"
	// RepoScope, when set with GitHub App auth, restricts the minted installation token to
	// that single repository (least privilege). Used by run-once per-repo collection; it
	// has no effect with PAT auth.
	RepoScope string
	// CollectWorkflows enables recent GitHub Actions workflow-run collection in SnapshotRepo.
	CollectWorkflows bool
	// WorkflowLookback bounds how far back workflow runs are counted (default 7 days).
	WorkflowLookback time.Duration
	// CollectLifecycle enables resolved-alert collection (MTTR + state) in SnapshotRepo.
	CollectLifecycle bool
	// ResolutionWindow bounds how far back resolved alerts are collected (default 90 days).
	ResolutionWindow time.Duration
	CodeScanningTool string // SARIF tool filter; empty counts all tools
	BaseURL          string // REST base URL override (must accept a trailing slash)
	GraphQLURL       string // GraphQL endpoint override
	HTTPClient       *http.Client
}

// Provider polls a GitHub organization or user for review items and security findings.
type Provider struct {
	rest             *gh.Client
	graphql          *graphqlClient
	toolName         string
	targetType       string
	maxPages         int
	collectWorkflows bool
	workflowLookback time.Duration
	collectLifecycle bool
	resolutionWindow time.Duration
}

// defaultWorkflowLookback bounds recent workflow-run collection when unset.
const defaultWorkflowLookback = 7 * 24 * time.Hour

// defaultResolutionWindow bounds resolved-alert collection when unset.
const defaultResolutionWindow = 90 * 24 * time.Hour

var (
	_ provider.Provider        = (*Provider)(nil)
	_ provider.RepoSnapshotter = (*Provider)(nil)
)

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

	targetType := opts.TargetType
	if targetType == "" {
		targetType = targetOrg
	}

	lookback := opts.WorkflowLookback
	if lookback <= 0 {
		lookback = defaultWorkflowLookback
	}

	window := opts.ResolutionWindow
	if window <= 0 {
		window = defaultResolutionWindow
	}

	return &Provider{
		rest:             rest,
		graphql:          &graphqlClient{httpClient: httpClient, endpoint: endpoint},
		toolName:         opts.CodeScanningTool,
		targetType:       targetType,
		maxPages:         defaultMaxPages,
		collectWorkflows: opts.CollectWorkflows,
		workflowLookback: lookback,
		collectLifecycle: opts.CollectLifecycle,
		resolutionWindow: window,
	}, nil
}

// Name identifies the provider on the "provider" metric attribute.
func (p *Provider) Name() string { return "github" }

// Snapshot polls the target owner's (organization or user) repositories. It merges the
// GraphQL result (open PRs + Dependabot findings) with the REST code scanning and secret
// scanning results. Each failing source is recorded in SourceErrors and yields a partial
// snapshot; only when every source fails does Snapshot return an error.
func (p *Provider) Snapshot(ctx context.Context, target string) (provider.Snapshot, error) {
	gql, gqlErr := p.collectGraphQL(ctx, target)
	cs, csErr := p.collectCodeScanning(ctx, target)
	ss, ssErr := p.collectSecretScanning(ctx, target)

	if gqlErr != nil && csErr != nil && ssErr != nil {
		return provider.Snapshot{}, fmt.Errorf("github: all sources failed for %q: %w", target, errors.Join(gqlErr, csErr, ssErr))
	}

	snap := provider.Snapshot{Repos: mergeRepos(gql.repos, cs.findings, ss.findings)}
	if gql.rateKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceGraphQL, Remaining: gql.rateRemaining})
	}
	// Code scanning and secret scanning share the REST rate-limit budget; report it once.
	restRate, restKnown := cs.rate, cs.rateKnown
	if !restKnown && ss.rateKnown {
		restRate, restKnown = ss.rate, true
	}
	if restKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceREST, Remaining: restRate})
	}
	if gqlErr != nil {
		zlog.Warn().Err(gqlErr).Str("provider", "github").Str("source", provider.SourceGraphQL).Str("target", target).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL})
	}
	if csErr != nil {
		zlog.Warn().Err(csErr).Str("provider", "github").Str("source", provider.SourceREST).Str("target", target).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceREST})
	}
	if ssErr != nil {
		zlog.Warn().Err(ssErr).Str("provider", "github").Str("source", provider.SourceSecretScanning).Str("target", target).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceSecretScanning})
	}
	zlog.Debug().Str("provider", "github").Str("target", target).
		Int("repos", len(snap.Repos)).Int("rate_limits", len(snap.RateLimits)).
		Msg("github snapshot assembled")
	return snap, nil
}

// SnapshotRepo polls a single repository (the per-repo collection path used by
// operator-dispatched run-once Jobs). It mirrors Snapshot but scopes every source to one
// repo: GraphQL for open PRs, Dependabot findings, and posture; REST for code scanning and
// secret scanning. Each failing source is recorded in SourceErrors and yields a partial
// snapshot; only when every source fails does it return an error.
func (p *Provider) SnapshotRepo(ctx context.Context, owner, repo string) (provider.Snapshot, error) {
	gql, gqlRate, gqlKnown, gqlErr := p.collectRepoGraphQL(ctx, owner, repo)
	csFindings, csRate, csKnown, _, csErr := p.codeScanningForRepo(ctx, owner, repo)
	ssFindings, ssRate, ssKnown, _, ssErr := p.secretScanningForRepo(ctx, owner, repo)

	if gqlErr != nil && csErr != nil && ssErr != nil {
		return provider.Snapshot{}, fmt.Errorf("github: all sources failed for %s/%s: %w", owner, repo, errors.Join(gqlErr, csErr, ssErr))
	}

	var gqlRepos []graphqlRepo
	if gqlErr == nil {
		gqlRepos = []graphqlRepo{gql}
	}
	snap := provider.Snapshot{Repos: mergeRepos(gqlRepos,
		map[string][]provider.Finding{repo: csFindings},
		map[string][]provider.Finding{repo: ssFindings},
	)}
	if gqlKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceGraphQL, Remaining: gqlRate})
	}
	// Code scanning and secret scanning share the REST rate-limit budget; report it once.
	restRate, restKnown := csRate, csKnown
	if !restKnown && ssKnown {
		restRate, restKnown = ssRate, true
	}
	if restKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceREST, Remaining: restRate})
	}
	if gqlErr != nil {
		zlog.Warn().Err(gqlErr).Str("provider", "github").Str("source", provider.SourceGraphQL).Str("repo", owner+"/"+repo).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL})
	}
	if csErr != nil {
		zlog.Warn().Err(csErr).Str("provider", "github").Str("source", provider.SourceREST).Str("repo", owner+"/"+repo).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceREST})
	}
	if ssErr != nil {
		zlog.Warn().Err(ssErr).Str("provider", "github").Str("source", provider.SourceSecretScanning).Str("repo", owner+"/"+repo).
			Msg("source failed; snapshot is partial")
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceSecretScanning})
	}
	// Workflow-run collection is opt-in and supplementary: never fatal, and a failure is a
	// partial (recorded) source error, not a lost snapshot.
	if p.collectWorkflows {
		if stats, wfErr := p.collectWorkflowRuns(ctx, owner, repo); wfErr != nil {
			zlog.Warn().Err(wfErr).Str("provider", "github").Str("source", provider.SourceWorkflows).Str("repo", owner+"/"+repo).
				Msg("source failed; snapshot is partial")
			snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceWorkflows})
		} else if len(snap.Repos) > 0 {
			snap.Repos[0].WorkflowRuns = stats
		}
	}
	// Lifecycle (resolved-alert) collection is opt-in and supplementary: never fatal.
	if p.collectLifecycle {
		if resolved, lcErr := p.collectResolvedFindings(ctx, owner, repo); lcErr != nil {
			zlog.Warn().Err(lcErr).Str("provider", "github").Str("source", provider.SourceLifecycle).Str("repo", owner+"/"+repo).
				Msg("source failed; snapshot is partial")
			snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceLifecycle})
		} else if len(snap.Repos) > 0 {
			snap.Repos[0].ResolvedFindings = resolved
		}
	}
	zlog.Debug().Str("provider", "github").Str("repo", owner+"/"+repo).
		Int("repos", len(snap.Repos)).Int("rate_limits", len(snap.RateLimits)).Msg("github repo snapshot assembled")
	return snap, nil
}

// mergeRepos combines the GraphQL repositories (open PRs + Dependabot findings) with any
// number of finding maps (code scanning, secret scanning), each keyed by repo name, into a
// deterministic slice sorted by repository name with each repo's findings sorted by
// category then severity.
func mergeRepos(gqlRepos []graphqlRepo, findingMaps ...map[string][]provider.Finding) []provider.RepoMetrics {
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
		r.Posture = gr.posture
	}
	for _, fm := range findingMaps {
		for name, fs := range fm {
			get(name).Findings = append(get(name).Findings, fs...)
		}
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
		if opts.RepoScope != "" {
			// Scope the minted installation token to the single repository (least privilege).
			itr.InstallationTokenOptions = &ghv88.InstallationTokenOptions{Repositories: []string{opts.RepoScope}}
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
