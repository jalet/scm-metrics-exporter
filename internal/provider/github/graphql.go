package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// orgMetricsQuery batches open PR counts and Dependabot alerts per page of repos,
// and reads the remaining GraphQL rate-limit quota from the same request.
const orgMetricsQuery = `query OrgMetrics($org: String!, $cursor: String) {
  organization(login: $org) {
    repositories(first: 50, after: $cursor) {
      pageInfo { hasNextPage endCursor }
      nodes {
        name
        pullRequests(states: OPEN) { totalCount }
        vulnerabilityAlerts(states: OPEN, first: 100) {
          nodes { securityVulnerability { severity } }
        }
      }
    }
  }
  rateLimit { remaining }
}`

type graphqlClient struct {
	httpClient *http.Client
	endpoint   string
}

// graphqlResponse mirrors the JSON envelope of orgMetricsQuery.
type graphqlResponse struct {
	Data struct {
		Organization struct {
			Repositories struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Name         string `json:"name"`
					PullRequests struct {
						TotalCount int `json:"totalCount"`
					} `json:"pullRequests"`
					VulnerabilityAlerts struct {
						Nodes []struct {
							SecurityVulnerability struct {
								Severity string `json:"severity"`
							} `json:"securityVulnerability"`
						} `json:"nodes"`
					} `json:"vulnerabilityAlerts"`
				} `json:"nodes"`
			} `json:"repositories"`
		} `json:"organization"`
		RateLimit struct {
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
}

type graphqlResult struct {
	repos         []graphqlRepo
	rateRemaining int64
	rateKnown     bool
}

// do executes the query for one page. A non-2xx status or a transport error is
// returned as an error; GraphQL-level errors are surfaced via graphqlResponse.Errors.
func (c *graphqlClient) do(ctx context.Context, org string, cursor *string) (*graphqlResponse, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     orgMetricsQuery,
		"variables": map[string]any{"org": org, "cursor": cursor},
	})
	if err != nil {
		return nil, fmt.Errorf("github graphql: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("github graphql: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req) //nolint:gosec // endpoint is operator-configured (GraphQLURL), not attacker input
	if err != nil {
		return nil, fmt.Errorf("github graphql: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github graphql: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github graphql: http %d: %s", resp.StatusCode, truncate(body))
	}

	var gr graphqlResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("github graphql: decode response: %w", err)
	}
	return &gr, nil
}

// collectGraphQL pages through the organization's repositories, accumulating open PR
// counts and Dependabot findings and the latest rate-limit reading. On error it
// returns whatever was collected so far.
func (p *Provider) collectGraphQL(ctx context.Context, org string) (graphqlResult, error) {
	var res graphqlResult
	var cursor *string
	for page := 0; page < p.maxPages; page++ {
		gr, err := p.graphql.do(ctx, org, cursor)
		if err != nil {
			return res, err
		}
		if len(gr.Errors) > 0 {
			return res, graphqlErrorsErr(gr.Errors)
		}

		res.rateRemaining = gr.Data.RateLimit.Remaining
		res.rateKnown = true
		res.repos = append(res.repos, mapGraphQLRepos(gr)...)

		pi := gr.Data.Organization.Repositories.PageInfo
		if !pi.HasNextPage {
			return res, nil
		}
		cursor = &pi.EndCursor
	}
	zlog.Warn().Str("org", org).Int("maxPages", p.maxPages).Msg("github graphql pagination cap reached")
	return res, nil
}

// mapGraphQLRepos maps a decoded response page to repos. It never panics on
// arbitrary input (fuzz target).
func mapGraphQLRepos(gr *graphqlResponse) []graphqlRepo {
	nodes := gr.Data.Organization.Repositories.Nodes
	repos := make([]graphqlRepo, 0, len(nodes))
	for _, n := range nodes {
		repo := graphqlRepo{name: n.Name, openPRs: n.PullRequests.TotalCount}
		for _, a := range n.VulnerabilityAlerts.Nodes {
			repo.findings = append(repo.findings, provider.Finding{
				Severity: provider.NormalizeSeverity(a.SecurityVulnerability.Severity),
				Category: provider.CategoryDependency,
			})
		}
		repos = append(repos, repo)
	}
	return repos
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
