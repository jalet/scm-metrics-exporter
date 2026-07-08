package github

import (
	"context"
	"slices"
	"strings"
	"time"

	gh "github.com/google/go-github/v89/github"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// collectWorkflowRuns tallies one repository's completed GitHub Actions runs within the
// lookback window, grouped by (workflow name, conclusion). Runs still in progress (no
// conclusion) are skipped. Pagination is bounded by maxPages. The result is sorted for a
// stable, low-cardinality series set.
func (p *Provider) collectWorkflowRuns(ctx context.Context, owner, repo string) ([]provider.WorkflowRunStat, error) {
	since := time.Now().Add(-p.workflowLookback).UTC().Format("2006-01-02")
	opts := &gh.ListWorkflowRunsOptions{Created: ">=" + since}
	opts.PerPage = 100

	tally := map[string]map[string]int{} // workflow -> conclusion -> count
	total := 0
	for page := 0; page < p.maxPages; page++ {
		runs, resp, err := p.rest.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		if err != nil {
			return nil, err
		}
		for _, run := range runs.WorkflowRuns {
			conclusion := strings.ToLower(run.GetConclusion())
			if conclusion == "" {
				continue // queued or in progress: not yet an outcome
			}
			name := run.GetName()
			if name == "" {
				name = "unknown"
			}
			if tally[name] == nil {
				tally[name] = map[string]int{}
			}
			tally[name][conclusion]++
			total++
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	stats := make([]provider.WorkflowRunStat, 0, len(tally))
	names := make([]string, 0, len(tally))
	for n := range tally {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		conclusions := make([]string, 0, len(tally[n]))
		for c := range tally[n] {
			conclusions = append(conclusions, c)
		}
		slices.Sort(conclusions)
		for _, c := range conclusions {
			stats = append(stats, provider.WorkflowRunStat{Workflow: n, Conclusion: c, Count: tally[n][c]})
		}
	}
	zlog.Debug().Str("provider", "github").Str("source", "workflows").Str("repo", owner+"/"+repo).
		Int("workflows", len(tally)).Int("runs", total).Msg("workflow runs collected")
	return stats, nil
}
