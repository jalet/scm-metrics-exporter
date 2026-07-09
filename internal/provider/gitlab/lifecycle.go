package gitlab

import (
	"context"
	"strings"
	"time"

	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// projectResolvedVulnsQuery reads one project's resolved and dismissed findings, newest
// first, with the timestamps and dismissal reason needed to bucket remediation.
const projectResolvedVulnsQuery = `query ProjectResolvedVulns($fullPath: ID!, $after: String) {
  project(fullPath: $fullPath) {
    vulnerabilities(
      state: [RESOLVED, DISMISSED]
      reportType: [SAST, DEPENDENCY_SCANNING, CONTAINER_SCANNING, SECRET_DETECTION, CLUSTER_IMAGE_SCANNING, CONTAINER_SCANNING_FOR_REGISTRY]
      sort: detected_desc
      first: 100
      after: $after
    ) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id severity reportType state detectedAt resolvedAt dismissedAt
        stateComment
        dismissalReason
      }
    }
  }
}`

type projectResolvedVulnResponse struct {
	Data struct {
		Project *struct {
			Vulnerabilities *struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID              string  `json:"id"`
					Severity        string  `json:"severity"`
					ReportType      string  `json:"reportType"`
					State           string  `json:"state"`
					DetectedAt      string  `json:"detectedAt"`
					ResolvedAt      *string `json:"resolvedAt"`
					DismissedAt     *string `json:"dismissedAt"`
					DismissalReason *string `json:"dismissalReason"`
				} `json:"nodes"`
			} `json:"vulnerabilities"`
		} `json:"project"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

// collectResolvedFindings pages a project's resolved/dismissed findings up to maxPages,
// filtering each node to those resolved within the window. The connection is sorted by
// detection time, not resolution time, so a node's resolution time gives no information
// about later pages: paging always continues based on pageInfo.hasNextPage alone.
func (p *Provider) collectResolvedFindings(ctx context.Context, path string) ([]provider.ResolvedFinding, error) {
	since := time.Now().Add(-p.resolutionWindow)
	var out []provider.ResolvedFinding
	var after *string
	for page := 0; page < p.maxPages; page++ {
		var gr projectResolvedVulnResponse
		_, _, err := p.graphql.post(ctx, projectResolvedVulnsQuery, map[string]any{"fullPath": path, "after": after}, &gr)
		if err != nil {
			return nil, err
		}
		if len(gr.Errors) > 0 {
			return nil, graphqlErrorsErr(gr.Errors)
		}
		if gr.Data.Project == nil || gr.Data.Project.Vulnerabilities == nil {
			return out, nil // non-Ultimate / no access: nothing to collect, not an error
		}
		for _, n := range gr.Data.Project.Vulnerabilities.Nodes {
			cat, ok := reportTypeCategory[n.ReportType]
			if !ok {
				continue
			}
			resolvedAt := gitlabResolvedAt(n.State, n.ResolvedAt, n.DismissedAt)
			if resolvedAt.Before(since) {
				continue
			}
			out = append(out, provider.ResolvedFinding{
				ID:         n.ID,
				Category:   cat,
				Severity:   provider.NormalizeSeverity(n.Severity),
				State:      gitlabState(n.State),
				Resolution: gitlabResolution(n.State, deref(n.DismissalReason)),
				CreatedAt:  parseGitLabTime(n.DetectedAt),
				ResolvedAt: resolvedAt,
			})
		}
		pi := gr.Data.Project.Vulnerabilities.PageInfo
		if !pi.HasNextPage {
			break
		}
		after = &pi.EndCursor
	}
	zlog.Debug().Str("provider", "gitlab").Str("source", provider.SourceLifecycle).Str("project", path).
		Int("resolved", len(out)).Msg("resolved findings collected")
	return out, nil
}

func gitlabResolvedAt(state string, resolvedAt, dismissedAt *string) time.Time {
	if strings.EqualFold(state, "RESOLVED") && resolvedAt != nil {
		return parseGitLabTime(*resolvedAt)
	}
	if dismissedAt != nil {
		return parseGitLabTime(*dismissedAt)
	}
	if resolvedAt != nil {
		return parseGitLabTime(*resolvedAt)
	}
	return time.Time{}
}

func gitlabState(state string) string {
	if strings.EqualFold(state, "RESOLVED") {
		return provider.StateResolved
	}
	return provider.StateDismissed
}

// gitlabResolution maps a GitLab (state, dismissalReason) to a normalized resolution.
func gitlabResolution(state, dismissalReason string) string {
	if strings.EqualFold(state, "RESOLVED") {
		return provider.ResolutionFixed
	}
	switch strings.ToLower(strings.TrimSpace(dismissalReason)) {
	case "false_positive", "used_in_tests", "not_applicable":
		return provider.ResolutionDismissedNotARisk
	default: // acceptable_risk, mitigating_control, unexpected
		return provider.ResolutionDismissedAcceptedRisk
	}
}

func parseGitLabTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
