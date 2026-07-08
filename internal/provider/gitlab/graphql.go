package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// groupVulnsQuery reads the group's open findings in one Relay-paginated traversal,
// server-side filtered to open states and the report types we map to categories.
const groupVulnsQuery = `query GroupVulns($fullPath: ID!, $after: String) {
  group(fullPath: $fullPath) {
    vulnerabilities(
      state: [DETECTED, CONFIRMED]
      reportType: [SAST, DEPENDENCY_SCANNING, CONTAINER_SCANNING, SECRET_DETECTION, CLUSTER_IMAGE_SCANNING, CONTAINER_SCANNING_FOR_REGISTRY]
      first: 100
      after: $after
    ) {
      pageInfo { hasNextPage endCursor }
      nodes { severity reportType scanner { name } project { fullPath } }
    }
  }
}`

// groupProjectsQuery pages the group's projects (including subgroups) for security
// posture. visibility and archived are cheap project fields; branchRules and
// securityScanners give the default-branch protection and dependency-scanning state
// without a per-project call. All of it rides one Relay-paginated traversal.
const groupProjectsQuery = `query GroupProjects($fullPath: ID!, $after: String) {
  group(fullPath: $fullPath) {
    projects(includeSubgroups: true, first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        fullPath
        visibility
        archived
        securityScanners { enabled }
        branchRules(first: 50) { nodes { isDefault isProtected } }
      }
    }
  }
}`

// scannerDependencyScanning is the securityScanners.enabled value that maps to the
// provider-neutral "dependency alerting enabled" posture bit (GitLab's analogue of
// GitHub Dependabot alerts).
const scannerDependencyScanning = "DEPENDENCY_SCANNING"

// reportTypeCategory maps GitLab report types to our finding categories (aligned with
// GitLab's own feature categories). Unmapped types (dast, api_fuzzing, coverage_fuzzing,
// sarif, generic) are skipped rather than bucketed.
var reportTypeCategory = map[string]string{
	"SAST":                            provider.CategoryStaticAnalysis,
	"DEPENDENCY_SCANNING":             provider.CategoryDependency,
	"SECRET_DETECTION":                provider.CategorySecret,
	"CONTAINER_SCANNING":              provider.CategoryContainer,
	"CLUSTER_IMAGE_SCANNING":          provider.CategoryContainer,
	"CONTAINER_SCANNING_FOR_REGISTRY": provider.CategoryContainer,
}

type graphqlClient struct {
	httpClient *http.Client
	endpoint   string
}

// vulnResponse mirrors the JSON envelope. group and vulnerabilities are pointers so a
// null (non-Ultimate instance or missing security access) is distinguishable from a
// genuine empty node list.
type vulnResponse struct {
	Data struct {
		Group *struct {
			Vulnerabilities *struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Severity   string `json:"severity"`
					ReportType string `json:"reportType"`
					Scanner    struct {
						Name string `json:"name"`
					} `json:"scanner"`
					Project struct {
						FullPath string `json:"fullPath"`
					} `json:"project"`
				} `json:"nodes"`
			} `json:"vulnerabilities"`
		} `json:"group"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

type graphqlError struct {
	Message string `json:"message"`
}

type vulnResult struct {
	findings  map[string][]provider.Finding // keyed by project fullPath (== path_with_namespace)
	rate      int64
	rateKnown bool
}

// projectsResponse mirrors the JSON envelope of groupProjectsQuery. group and projects
// are pointers so a null (missing access) is distinguishable from an empty node list;
// securityScanners and branchRules are pointers for the same reason.
type projectsResponse struct {
	Data struct {
		Group *struct {
			Projects *struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					FullPath         string `json:"fullPath"`
					Visibility       string `json:"visibility"`
					Archived         bool   `json:"archived"`
					SecurityScanners *struct {
						Enabled []string `json:"enabled"`
					} `json:"securityScanners"`
					BranchRules *struct {
						Nodes []struct {
							IsDefault   bool `json:"isDefault"`
							IsProtected bool `json:"isProtected"`
						} `json:"nodes"`
					} `json:"branchRules"`
				} `json:"nodes"`
			} `json:"projects"`
		} `json:"group"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

type postureResult struct {
	posture   map[string]*provider.RepoPosture // keyed by project fullPath (== path_with_namespace)
	rate      int64
	rateKnown bool
}

// collectVulnerabilities pages the group's open findings. A null group or null
// vulnerabilities field, or any top-level GraphQL error, is treated as "security source
// unavailable" and returned as an error (so the caller records a graphql SourceError
// and preserves MR data). A present-but-empty node list is a genuine zero.
func (p *Provider) collectVulnerabilities(ctx context.Context, group string) (vulnResult, error) {
	res := vulnResult{findings: make(map[string][]provider.Finding)}
	var after *string
	for page := 0; page < p.maxPages; page++ {
		gr, rate, rateKnown, err := p.graphql.do(ctx, group, after)
		if rateKnown {
			res.rate, res.rateKnown = rate, true
		}
		if err != nil {
			return res, err
		}
		if len(gr.Errors) > 0 {
			return res, graphqlErrorsErr(gr.Errors)
		}
		if gr.Data.Group == nil || gr.Data.Group.Vulnerabilities == nil {
			return res, fmt.Errorf("gitlab graphql: vulnerabilities unavailable for group %q (non-Ultimate or missing security access)", group)
		}
		mapVulnerabilities(gr, res.findings)
		zlog.Debug().Str("provider", "gitlab").Str("source", "graphql").Str("group", group).
			Int("page", page).Int("nodes_in_page", len(gr.Data.Group.Vulnerabilities.Nodes)).
			Int("repos_with_findings", len(res.findings)).Msg("fetched vulnerabilities page")

		pi := gr.Data.Group.Vulnerabilities.PageInfo
		if !pi.HasNextPage {
			zlog.Debug().Str("provider", "gitlab").Str("source", "graphql").Str("group", group).
				Int("repos_with_findings", len(res.findings)).Int("pages", page+1).Msg("vulnerabilities collection complete")
			return res, nil
		}
		after = &pi.EndCursor
	}
	zlog.Warn().Str("group", group).Int("maxPages", p.maxPages).Msg("gitlab graphql pagination cap reached")
	return res, nil
}

// mapVulnerabilities appends findings, skipping unmapped report types. It is nil-safe on
// every pointer so it never panics on arbitrary decoded input (fuzz target).
func mapVulnerabilities(gr *vulnResponse, into map[string][]provider.Finding) {
	if gr.Data.Group == nil || gr.Data.Group.Vulnerabilities == nil {
		return
	}
	for _, n := range gr.Data.Group.Vulnerabilities.Nodes {
		cat, ok := reportTypeCategory[n.ReportType]
		if !ok {
			continue
		}
		into[n.Project.FullPath] = append(into[n.Project.FullPath], provider.Finding{
			Severity: provider.NormalizeSeverity(n.Severity),
			Category: cat,
			Tool:     n.Scanner.Name,
		})
	}
}

func (c *graphqlClient) do(ctx context.Context, group string, after *string) (*vulnResponse, int64, bool, error) {
	var gr vulnResponse
	rate, rateKnown, err := c.post(ctx, groupVulnsQuery, map[string]any{"fullPath": group, "after": after}, &gr)
	return &gr, rate, rateKnown, err
}

// post executes one GraphQL query and unmarshals the response envelope into out. It
// returns the RateLimit-Remaining reading when present. A transport error, a non-2xx
// status, or a decode failure is returned as an error; GraphQL-level errors surface via
// the decoded envelope's Errors field (checked by the caller).
func (c *graphqlClient) post(ctx context.Context, query string, vars map[string]any, out any) (int64, bool, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return 0, false, fmt.Errorf("gitlab graphql: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, false, fmt.Errorf("gitlab graphql: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req) //nolint:gosec // endpoint is operator-configured (GraphQLURL), not attacker input
	if err != nil {
		return 0, false, fmt.Errorf("gitlab graphql: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var rate int64
	var rateKnown bool
	if v := resp.Header.Get("RateLimit-Remaining"); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			rate, rateKnown = n, true
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return rate, rateKnown, fmt.Errorf("gitlab graphql: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return rate, rateKnown, fmt.Errorf("gitlab graphql: http %d: %s", resp.StatusCode, truncate(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return rate, rateKnown, fmt.Errorf("gitlab graphql: decode response: %w", err)
	}
	return rate, rateKnown, nil
}

// collectProjectPosture pages the group's projects for security posture, keyed by project
// fullPath. A null group or null projects field, or any top-level GraphQL error, is
// treated as "posture unavailable" and returned as an error so the caller records a
// graphql SourceError while keeping the rest of the snapshot. Posture is supplementary:
// its failure never fails the whole snapshot.
func (p *Provider) collectProjectPosture(ctx context.Context, group string) (postureResult, error) {
	res := postureResult{posture: make(map[string]*provider.RepoPosture)}
	var after *string
	for page := 0; page < p.maxPages; page++ {
		var gr projectsResponse
		rate, rateKnown, err := p.graphql.post(ctx, groupProjectsQuery, map[string]any{"fullPath": group, "after": after}, &gr)
		if rateKnown {
			res.rate, res.rateKnown = rate, true
		}
		if err != nil {
			return res, err
		}
		if len(gr.Errors) > 0 {
			return res, graphqlErrorsErr(gr.Errors)
		}
		if gr.Data.Group == nil || gr.Data.Group.Projects == nil {
			return res, fmt.Errorf("gitlab graphql: projects unavailable for group %q", group)
		}
		mapProjectPosture(&gr, res.posture)
		zlog.Debug().Str("provider", "gitlab").Str("source", "graphql").Str("group", group).
			Int("page", page).Int("nodes_in_page", len(gr.Data.Group.Projects.Nodes)).
			Int("repos_with_posture", len(res.posture)).Msg("fetched projects posture page")

		pi := gr.Data.Group.Projects.PageInfo
		if !pi.HasNextPage {
			zlog.Debug().Str("provider", "gitlab").Str("source", "graphql").Str("group", group).
				Int("repos_with_posture", len(res.posture)).Int("pages", page+1).Msg("posture collection complete")
			return res, nil
		}
		after = &pi.EndCursor
	}
	zlog.Warn().Str("group", group).Int("maxPages", p.maxPages).Msg("gitlab graphql posture pagination cap reached")
	return res, nil
}

// mapProjectPosture fills the posture map from a decoded page. It is nil-safe on every
// pointer so it never panics on arbitrary decoded input (fuzz target). BranchProtected is
// true only when a branch rule both protects and is the default-branch rule, so a
// protected non-default rule does not count as "default branch protected".
func mapProjectPosture(gr *projectsResponse, into map[string]*provider.RepoPosture) {
	if gr.Data.Group == nil || gr.Data.Group.Projects == nil {
		return
	}
	for _, n := range gr.Data.Group.Projects.Nodes {
		ps := &provider.RepoPosture{
			Visibility: strings.ToLower(n.Visibility),
			Archived:   n.Archived,
		}
		if n.SecurityScanners != nil {
			for _, s := range n.SecurityScanners.Enabled {
				if strings.EqualFold(s, scannerDependencyScanning) {
					ps.DependabotEnabled = true
					break
				}
			}
		}
		if n.BranchRules != nil {
			for _, br := range n.BranchRules.Nodes {
				if br.IsDefault && br.IsProtected {
					ps.BranchProtected = true
					break
				}
			}
		}
		into[n.FullPath] = ps
	}
}

func graphqlErrorsErr(errs []graphqlError) error {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return fmt.Errorf("gitlab graphql errors: %s", strings.Join(msgs, "; "))
}

func truncate(b []byte) string {
	const maxBody = 256
	if len(b) > maxBody {
		return string(b[:maxBody]) + "..."
	}
	return string(b)
}
