package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// ownerMetricsQuery batches open PR counts and Dependabot alerts per page of repos,
// and reads the remaining GraphQL rate-limit quota from the same request. It uses
// repositoryOwner(login:), which resolves either an organization or a user, so one
// query serves both target types.
const ownerMetricsQuery = `query OwnerMetrics($login: String!, $cursor: String) {
  repositoryOwner(login: $login) {
    repositories(first: 50, after: $cursor) {
      pageInfo { hasNextPage endCursor }
      nodes {
        name
        isArchived
        visibility
        hasVulnerabilityAlertsEnabled
        defaultBranchRef { branchProtectionRule { id } rules(first: 1) { totalCount } }
        pullRequests(states: OPEN) { totalCount }
        vulnerabilityAlerts(states: OPEN, first: 100) {
          nodes { createdAt securityVulnerability { severity package { ecosystem } } }
        }
      }
    }
  }
  rateLimit { remaining }
}`

// repoMetricsQuery fetches the same per-repository metrics as ownerMetricsQuery for a
// single repository. It powers the per-repo collection path: repository(owner:,name:)
// resolves one repo, so an operator-dispatched Job collects exactly its target.
const repoMetricsQuery = `query RepoMetrics($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    name
    isArchived
    visibility
    hasVulnerabilityAlertsEnabled
    defaultBranchRef { branchProtectionRule { id } rules(first: 1) { totalCount } }
    pullRequests(states: OPEN) { totalCount }
    vulnerabilityAlerts(states: OPEN, first: 100) {
      nodes { createdAt securityVulnerability { severity package { ecosystem } } }
    }
  }
  rateLimit { remaining }
}`

type graphqlClient struct {
	httpClient *http.Client
	endpoint   string
}

// repoNode is one repository's fields, shared by the owner-wide query (as a repeated
// node) and the single-repo query (as the sole repository).
type repoNode struct {
	Name                          string `json:"name"`
	IsArchived                    bool   `json:"isArchived"`
	Visibility                    string `json:"visibility"`
	HasVulnerabilityAlertsEnabled bool   `json:"hasVulnerabilityAlertsEnabled"`
	DefaultBranchRef              *struct {
		BranchProtectionRule *struct {
			ID string `json:"id"`
		} `json:"branchProtectionRule"`
		Rules struct {
			TotalCount int `json:"totalCount"`
		} `json:"rules"`
	} `json:"defaultBranchRef"`
	PullRequests struct {
		TotalCount int `json:"totalCount"`
	} `json:"pullRequests"`
	VulnerabilityAlerts struct {
		Nodes []struct {
			CreatedAt             string `json:"createdAt"`
			SecurityVulnerability struct {
				Severity string `json:"severity"`
				Package  struct {
					Ecosystem string `json:"ecosystem"`
				} `json:"package"`
			} `json:"securityVulnerability"`
		} `json:"nodes"`
	} `json:"vulnerabilityAlerts"`
}

// graphqlResponse mirrors the JSON envelope of ownerMetricsQuery.
type graphqlResponse struct {
	Data struct {
		RepositoryOwner struct {
			Repositories struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []repoNode `json:"nodes"`
			} `json:"repositories"`
		} `json:"repositoryOwner"`
		RateLimit struct {
			Remaining int64 `json:"remaining"`
		} `json:"rateLimit"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

// repoResponse mirrors the JSON envelope of repoMetricsQuery. Repository is a pointer so a
// null (repo not found or no access) is distinguishable from a genuine empty object.
type repoResponse struct {
	Data struct {
		Repository *repoNode `json:"repository"`
		RateLimit  struct {
			Remaining int64 `json:"remaining"`
		} `json:"rateLimit"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

type graphqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type graphqlRepo struct {
	name     string
	openPRs  int
	findings []provider.Finding
	posture  *provider.RepoPosture
}

type graphqlResult struct {
	repos         []graphqlRepo
	rateRemaining int64
	rateKnown     bool
}

// do executes the owner-wide query for one page. A non-2xx status or a transport error is
// returned as an error; GraphQL-level errors are surfaced via graphqlResponse.Errors.
func (c *graphqlClient) do(ctx context.Context, owner string, cursor *string) (*graphqlResponse, error) {
	var gr graphqlResponse
	if err := c.post(ctx, ownerMetricsQuery, map[string]any{"login": owner, "cursor": cursor}, &gr); err != nil {
		return nil, err
	}
	return &gr, nil
}

// doRepo executes the single-repository query. Error semantics match do.
func (c *graphqlClient) doRepo(ctx context.Context, owner, name string) (*repoResponse, error) {
	var rr repoResponse
	if err := c.post(ctx, repoMetricsQuery, map[string]any{"owner": owner, "name": name}, &rr); err != nil {
		return nil, err
	}
	return &rr, nil
}

// post marshals one GraphQL query, executes it, and decodes the response envelope into
// out. A non-2xx status or a transport/decode error is returned as an error; GraphQL-level
// errors are surfaced via the envelope's Errors field (checked by the caller).
func (c *graphqlClient) post(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("github graphql: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github graphql: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req) //nolint:gosec // endpoint is operator-configured (GraphQLURL), not attacker input
	if err != nil {
		return fmt.Errorf("github graphql: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github graphql: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql: http %d: %s", resp.StatusCode, truncate(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("github graphql: decode response: %w", err)
	}
	return nil
}

// collectGraphQL pages through the owner's repositories (organization or user),
// accumulating open PR counts and Dependabot findings and the latest rate-limit
// reading. On error it returns whatever was collected so far.
func (p *Provider) collectGraphQL(ctx context.Context, owner string) (graphqlResult, error) {
	var res graphqlResult
	var cursor *string
	for page := 0; page < p.maxPages; page++ {
		gr, err := p.graphql.do(ctx, owner, cursor)
		if err != nil {
			return res, err
		}
		if len(gr.Errors) > 0 {
			return res, graphqlErrorsErr(gr.Errors)
		}

		res.rateRemaining = gr.Data.RateLimit.Remaining
		res.rateKnown = true
		pageRepos := mapGraphQLRepos(gr)
		res.repos = append(res.repos, pageRepos...)
		zlog.Debug().Str("provider", "github").Str("source", "graphql").Str("owner", owner).
			Int("page", page).Int("repos_in_page", len(pageRepos)).Int("repos_total", len(res.repos)).
			Int64("rate_remaining", res.rateRemaining).Msg("fetched repositories page")

		pi := gr.Data.RepositoryOwner.Repositories.PageInfo
		if !pi.HasNextPage {
			zlog.Debug().Str("provider", "github").Str("source", "graphql").Str("owner", owner).
				Int("repos", len(res.repos)).Int("pages", page+1).Msg("graphql collection complete")
			return res, nil
		}
		cursor = &pi.EndCursor
	}
	zlog.Warn().Str("owner", owner).Int("maxPages", p.maxPages).Msg("github graphql pagination cap reached")
	return res, nil
}

// collectRepoGraphQL fetches one repository's open PR count, Dependabot findings, posture,
// and the GraphQL rate reading. A null repository (not found or no access) or a GraphQL
// error is returned as an error so the caller records a source failure.
func (p *Provider) collectRepoGraphQL(ctx context.Context, owner, name string) (graphqlRepo, int64, bool, error) {
	rr, err := p.graphql.doRepo(ctx, owner, name)
	if err != nil {
		return graphqlRepo{}, 0, false, err
	}
	rate := rr.Data.RateLimit.Remaining
	if len(rr.Errors) > 0 {
		return graphqlRepo{}, rate, true, graphqlErrorsErr(rr.Errors)
	}
	if rr.Data.Repository == nil {
		return graphqlRepo{}, rate, true, fmt.Errorf("github graphql: repository %s/%s not found or inaccessible", owner, name)
	}
	return mapRepoNode(*rr.Data.Repository), rate, true, nil
}

// mapGraphQLRepos maps a decoded response page to repos. It never panics on
// arbitrary input (fuzz target).
func mapGraphQLRepos(gr *graphqlResponse) []graphqlRepo {
	nodes := gr.Data.RepositoryOwner.Repositories.Nodes
	repos := make([]graphqlRepo, 0, len(nodes))
	for _, n := range nodes {
		repos = append(repos, mapRepoNode(n))
	}
	return repos
}

// mapRepoNode maps one decoded repository node to a graphqlRepo (posture plus Dependabot
// findings). Shared by the owner-wide and single-repo collection paths.
func mapRepoNode(n repoNode) graphqlRepo {
	repo := graphqlRepo{name: n.Name, openPRs: n.PullRequests.TotalCount}
	repo.posture = &provider.RepoPosture{
		Visibility:        strings.ToLower(n.Visibility),
		Archived:          n.IsArchived,
		DependabotEnabled: n.HasVulnerabilityAlertsEnabled,
		BranchProtected: n.DefaultBranchRef != nil &&
			(n.DefaultBranchRef.BranchProtectionRule != nil || n.DefaultBranchRef.Rules.TotalCount > 0),
	}
	for _, a := range n.VulnerabilityAlerts.Nodes {
		repo.findings = append(repo.findings, provider.Finding{
			Severity:  provider.NormalizeSeverity(a.SecurityVulnerability.Severity),
			Category:  provider.CategoryDependency,
			Ecosystem: strings.ToLower(a.SecurityVulnerability.Package.Ecosystem),
			CreatedAt: parseGraphQLTime(a.CreatedAt),
		})
	}
	return repo
}

// parseGraphQLTime parses a GitHub GraphQL DateTime (RFC3339). An empty or unparseable
// value yields the zero time, which the open-age histogram excludes.
func parseGraphQLTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func graphqlErrorsErr(errs []graphqlError) error {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return fmt.Errorf("github graphql errors: %s", strings.Join(msgs, "; "))
}

func truncate(b []byte) string {
	const maxBody = 256
	if len(b) > maxBody {
		return string(b[:maxBody]) + "..."
	}
	return string(b)
}
