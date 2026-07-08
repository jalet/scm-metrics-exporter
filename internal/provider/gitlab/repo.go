package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// terminalPipelineStatuses are the GitLab pipeline statuses counted as outcomes; transient
// states (pending, running, created, ...) are skipped, mirroring GitHub's skip of
// in-progress runs.
var terminalPipelineStatuses = map[string]bool{
	"success": true, "failed": true, "canceled": true, "skipped": true,
}

var _ provider.RepoSnapshotter = (*Provider)(nil)

// projectVulnsQuery reads one project's open findings (Ultimate/security access only).
const projectVulnsQuery = `query ProjectVulns($fullPath: ID!, $after: String) {
  project(fullPath: $fullPath) {
    vulnerabilities(
      state: [DETECTED, CONFIRMED]
      reportType: [SAST, DEPENDENCY_SCANNING, CONTAINER_SCANNING, SECRET_DETECTION, CLUSTER_IMAGE_SCANNING, CONTAINER_SCANNING_FOR_REGISTRY]
      first: 100
      after: $after
    ) {
      pageInfo { hasNextPage endCursor }
      nodes { severity reportType scanner { name } }
    }
  }
}`

// projectPostureQuery reads one project's security posture.
const projectPostureQuery = `query ProjectPosture($fullPath: ID!) {
  project(fullPath: $fullPath) {
    visibility
    archived
    securityScanners { enabled }
    branchRules(first: 50) { nodes { isDefault isProtected } }
  }
}`

type projectVulnResponse struct {
	Data struct {
		Project *struct {
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
				} `json:"nodes"`
			} `json:"vulnerabilities"`
		} `json:"project"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

type projectPostureResponse struct {
	Data struct {
		Project *struct {
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
		} `json:"project"`
	} `json:"data"`
	Errors []graphqlError `json:"errors"`
}

// SnapshotRepo polls a single GitLab project, the per-project collection path used by
// operator-dispatched run-once Jobs. repo is the project's full path (path_with_namespace,
// == GraphQL fullPath); owner is unused because the full path identifies the project. It
// mirrors Snapshot scoped to one project: REST for the open merge-request count, GraphQL
// for vulnerability findings and posture. Each failing source is recorded in SourceErrors
// and yields a partial snapshot; only when every source fails does it return an error.
func (p *Provider) SnapshotRepo(ctx context.Context, _, repo string) (provider.Snapshot, error) {
	mrCount, restRate, restKnown, mrErr := p.collectRepoMRs(ctx, repo)
	findings, vulnRate, vulnKnown, vulnErr := p.collectRepoVulnerabilities(ctx, repo)
	posture, postRate, postKnown, postErr := p.collectRepoPosture(ctx, repo)

	if mrErr != nil && vulnErr != nil && postErr != nil {
		return provider.Snapshot{}, fmt.Errorf("gitlab: all sources failed for %q: %w", repo, errors.Join(mrErr, vulnErr, postErr))
	}

	rm := provider.RepoMetrics{Name: repo, OpenReviewItems: mrCount}
	rm.Findings = append(rm.Findings, findings...)
	sortFindings(rm.Findings)
	if postErr == nil {
		rm.Posture = posture
	}
	snap := provider.Snapshot{Repos: []provider.RepoMetrics{rm}}

	if restKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceREST, Remaining: restRate})
	}
	// Vulnerabilities and posture share the GraphQL budget; report it once (prefer posture).
	gqlRate, gqlKnown := vulnRate, vulnKnown
	if postKnown {
		gqlRate, gqlKnown = postRate, true
	}
	if gqlKnown {
		snap.RateLimits = append(snap.RateLimits, provider.RateLimit{Resource: provider.ResourceGraphQL, Remaining: gqlRate})
	}

	if mrErr != nil {
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceREST})
	}
	// A GraphQL outage can fail both vulnerabilities and posture; record one graphql error.
	if vulnErr != nil || postErr != nil {
		snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceGraphQL})
	}
	// Pipeline collection is opt-in and supplementary: never fatal, a failure is recorded.
	if p.collectWorkflows {
		if stats, plErr := p.collectPipelineRuns(ctx, repo); plErr != nil {
			snap.SourceErrors = append(snap.SourceErrors, provider.SourceError{Source: provider.SourceWorkflows})
		} else if len(snap.Repos) > 0 {
			snap.Repos[0].WorkflowRuns = stats
		}
	}
	return snap, nil
}

// collectPipelineRuns tallies one project's recent pipelines within the lookback window,
// grouped by (source, status). GitLab has no per-run "workflow name", so the pipeline
// source (push, schedule, merge_request_event, ...) is used as the workflow label and the
// pipeline status as the conclusion. Transient statuses are skipped; pagination is bounded.
func (p *Provider) collectPipelineRuns(ctx context.Context, path string) ([]provider.WorkflowRunStat, error) {
	since := time.Now().Add(-p.workflowLookback).UTC().Format(time.RFC3339)
	base := fmt.Sprintf("/projects/%s/pipelines?updated_after=%s&per_page=100", escapeGroup(path), url.QueryEscape(since))
	tally := map[string]map[string]int{} // source -> status -> count
	for page, next := 0, "1"; next != "" && page < p.maxPages; page++ {
		body, nextPage, _, _, err := p.rest.getPage(ctx, base+"&page="+next)
		if err != nil {
			return nil, err
		}
		var pipelines []struct {
			Status string `json:"status"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(body, &pipelines); err != nil {
			return nil, fmt.Errorf("gitlab rest: decode pipelines: %w", err)
		}
		for _, pl := range pipelines {
			status := strings.ToLower(pl.Status)
			if !terminalPipelineStatuses[status] {
				continue
			}
			source := pl.Source
			if source == "" {
				source = "unknown"
			}
			if tally[source] == nil {
				tally[source] = map[string]int{}
			}
			tally[source][status]++
		}
		next = nextPage
	}
	zlog.Debug().Str("provider", "gitlab").Str("source", "workflows").Str("project", path).
		Int("sources", len(tally)).Msg("pipelines collected")
	return provider.WorkflowRunStatsFromTally(tally), nil
}

// collectRepoMRs counts one project's open merge requests across pages.
func (p *Provider) collectRepoMRs(ctx context.Context, path string) (count int, rate int64, rateKnown bool, err error) {
	base := fmt.Sprintf("/projects/%s/merge_requests?state=opened&per_page=100", escapeGroup(path))
	for page, next := 0, "1"; next != "" && page < p.maxPages; page++ {
		body, nextPage, r, ok, e := p.rest.getPage(ctx, base+"&page="+next)
		if ok {
			rate, rateKnown = r, true
		}
		if e != nil {
			return count, rate, rateKnown, e
		}
		var mrs []json.RawMessage
		if e := json.Unmarshal(body, &mrs); e != nil {
			return count, rate, rateKnown, fmt.Errorf("gitlab rest: decode merge requests: %w", e)
		}
		count += len(mrs)
		next = nextPage
	}
	return count, rate, rateKnown, nil
}

// collectRepoVulnerabilities pages one project's open findings. A null project or
// vulnerabilities field (non-Ultimate or missing access) or a GraphQL error is returned as
// an error so the caller records a partial snapshot.
func (p *Provider) collectRepoVulnerabilities(ctx context.Context, path string) (findings []provider.Finding, rate int64, rateKnown bool, err error) {
	var after *string
	for page := 0; page < p.maxPages; page++ {
		var gr projectVulnResponse
		r, rk, e := p.graphql.post(ctx, projectVulnsQuery, map[string]any{"fullPath": path, "after": after}, &gr)
		if rk {
			rate, rateKnown = r, true
		}
		if e != nil {
			return findings, rate, rateKnown, e
		}
		if len(gr.Errors) > 0 {
			return findings, rate, rateKnown, graphqlErrorsErr(gr.Errors)
		}
		if gr.Data.Project == nil || gr.Data.Project.Vulnerabilities == nil {
			return findings, rate, rateKnown, fmt.Errorf("gitlab graphql: vulnerabilities unavailable for project %q (non-Ultimate or missing access)", path)
		}
		for _, n := range gr.Data.Project.Vulnerabilities.Nodes {
			cat, ok := reportTypeCategory[n.ReportType]
			if !ok {
				continue
			}
			findings = append(findings, provider.Finding{
				Severity: provider.NormalizeSeverity(n.Severity),
				Category: cat,
				Tool:     n.Scanner.Name,
			})
		}
		pi := gr.Data.Project.Vulnerabilities.PageInfo
		if !pi.HasNextPage {
			return findings, rate, rateKnown, nil
		}
		after = &pi.EndCursor
	}
	return findings, rate, rateKnown, nil
}

// collectRepoPosture reads one project's posture. A null project or a GraphQL error is
// returned as an error (posture is supplementary; the caller keeps the rest of the snapshot).
func (p *Provider) collectRepoPosture(ctx context.Context, path string) (*provider.RepoPosture, int64, bool, error) {
	var gr projectPostureResponse
	rate, rateKnown, err := p.graphql.post(ctx, projectPostureQuery, map[string]any{"fullPath": path}, &gr)
	if err != nil {
		return nil, rate, rateKnown, err
	}
	if len(gr.Errors) > 0 {
		return nil, rate, rateKnown, graphqlErrorsErr(gr.Errors)
	}
	if gr.Data.Project == nil {
		return nil, rate, rateKnown, fmt.Errorf("gitlab graphql: project %q not found or inaccessible", path)
	}
	pr := gr.Data.Project
	ps := &provider.RepoPosture{Visibility: strings.ToLower(pr.Visibility), Archived: pr.Archived}
	if pr.SecurityScanners != nil {
		for _, s := range pr.SecurityScanners.Enabled {
			if strings.EqualFold(s, scannerDependencyScanning) {
				ps.DependabotEnabled = true
				break
			}
		}
	}
	if pr.BranchRules != nil {
		for _, br := range pr.BranchRules.Nodes {
			if br.IsDefault && br.IsProtected {
				ps.BranchProtected = true
				break
			}
		}
	}
	return ps, rate, rateKnown, nil
}
