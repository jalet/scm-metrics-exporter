package github

import (
	"context"
	"errors"
	"net/http"

	gh "github.com/google/go-github/v89/github"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

type codeScanningResult struct {
	findings  map[string][]provider.Finding // keyed by repository name
	rate      int64
	rateKnown bool
}

// collectCodeScanning lists open code scanning alerts (optionally filtered to one SARIF
// tool) grouped by repository, mapping each to a static_analysis finding. Org targets use
// the single org-scoped endpoint; user targets have no such endpoint, so they iterate the
// user's repositories and call the per-repo endpoint. Any hard API error is returned so
// the caller records a source error; the rate-limit reading is captured when available.
func (p *Provider) collectCodeScanning(ctx context.Context, target string) (codeScanningResult, error) {
	if p.targetType == targetUser {
		return p.collectCodeScanningForUser(ctx, target)
	}
	return p.collectCodeScanningForOrg(ctx, target)
}

func (p *Provider) collectCodeScanningForOrg(ctx context.Context, org string) (codeScanningResult, error) {
	res := codeScanningResult{findings: make(map[string][]provider.Finding)}

	opts := &gh.AlertListOptions{State: "open", ToolName: p.toolName} // empty tool = all tools
	opts.ListOptions.PerPage = 100

	total := 0
	for page := 0; page < p.maxPages; page++ {
		alerts, resp, err := p.rest.CodeScanning.ListAlertsForOrg(ctx, org, opts)
		if resp != nil {
			res.rate = int64(resp.Rate.Remaining)
			res.rateKnown = true
		}
		if err != nil {
			return res, err
		}

		for _, a := range alerts {
			repo := a.GetRepository().GetName()
			if repo == "" {
				continue
			}
			res.findings[repo] = append(res.findings[repo], codeScanningFinding(a))
		}
		total += len(alerts)
		zlog.Debug().Str("provider", "github").Str("source", "rest").Str("owner", org).
			Int("page", page).Int("alerts_in_page", len(alerts)).Int64("rate_remaining", res.rate).
			Msg("fetched code scanning page")

		if resp == nil || resp.NextPage == 0 {
			zlog.Debug().Str("provider", "github").Str("source", "rest").Str("owner", org).
				Int("alerts", total).Int("repos_with_findings", len(res.findings)).Int("pages", page+1).
				Msg("code scanning collection complete")
			return res, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
	zlog.Warn().Str("owner", org).Int("maxPages", p.maxPages).Msg("github code scanning pagination cap reached")
	return res, nil
}

// collectCodeScanningForUser enumerates the user's repositories and lists each repo's open
// code scanning alerts, since GitHub has no user-scoped code-scanning endpoint. A repo
// without code scanning enabled (403/404) is skipped, not fatal; any other error fails the
// source. This is an N+1 REST pattern -- one repo list plus one call per repo.
func (p *Provider) collectCodeScanningForUser(ctx context.Context, user string) (codeScanningResult, error) {
	res := codeScanningResult{findings: make(map[string][]provider.Finding)}

	repos, err := p.listUserRepos(ctx, user, &res)
	if err != nil {
		return res, err
	}

	skipped := 0
	for _, repo := range repos {
		opts := &gh.AlertListOptions{State: "open", ToolName: p.toolName}
		opts.ListOptions.PerPage = 100
		for page := 0; page < p.maxPages; page++ {
			alerts, resp, err := p.rest.CodeScanning.ListAlertsForRepo(ctx, user, repo, opts)
			if resp != nil {
				res.rate = int64(resp.Rate.Remaining)
				res.rateKnown = true
			}
			if err != nil {
				if notAccessible(err) {
					skipped++
					break // code scanning disabled / inaccessible on this repo
				}
				return res, err
			}
			for _, a := range alerts {
				res.findings[repo] = append(res.findings[repo], codeScanningFinding(a))
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.ListOptions.Page = resp.NextPage
		}
	}
	zlog.Debug().Str("provider", "github").Str("source", "rest").Str("owner", user).
		Int("repos", len(repos)).Int("repos_skipped", skipped).Int("repos_with_findings", len(res.findings)).
		Msg("per-repo code scanning complete (user target)")
	return res, nil
}

// listUserRepos returns the user's owned repository names, updating the rate reading.
func (p *Provider) listUserRepos(ctx context.Context, user string, res *codeScanningResult) ([]string, error) {
	opts := &gh.RepositoryListByUserOptions{Type: "owner"}
	opts.PerPage = 100
	var repos []string
	for page := 0; page < p.maxPages; page++ {
		rs, resp, err := p.rest.Repositories.ListByUser(ctx, user, opts)
		if resp != nil {
			res.rate = int64(resp.Rate.Remaining)
			res.rateKnown = true
		}
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if n := r.GetName(); n != "" {
				repos = append(repos, n)
			}
		}
		if resp == nil || resp.NextPage == 0 {
			return repos, nil
		}
		opts.Page = resp.NextPage
	}
	zlog.Warn().Str("owner", user).Int("maxPages", p.maxPages).Msg("github user repo listing cap reached")
	return repos, nil
}

func codeScanningFinding(a *gh.Alert) provider.Finding {
	return provider.Finding{
		Severity: provider.NormalizeSeverity(codeScanningSeverity(a)),
		Category: provider.CategoryStaticAnalysis,
	}
}

// notAccessible reports whether err is a 403/404 API error (feature disabled or no access
// on a single repo), which for per-repo iteration is a skip rather than a source failure.
func notAccessible(err error) bool {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode == http.StatusForbidden || er.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// codeScanningSeverity prefers the security-severity level (critical/high/medium/low)
// and falls back to the SARIF rule severity, which NormalizeSeverity passes through.
func codeScanningSeverity(a *gh.Alert) string {
	if a.Rule == nil {
		return ""
	}
	if a.Rule.SecuritySeverityLevel != nil {
		return *a.Rule.SecuritySeverityLevel
	}
	if a.Rule.Severity != nil {
		return *a.Rule.Severity
	}
	return ""
}
