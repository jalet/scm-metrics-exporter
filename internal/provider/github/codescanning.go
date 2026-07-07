package github

import (
	"context"

	zlog "github.com/rs/zerolog/log"

	gh "github.com/google/go-github/v89/github"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

type codeScanningResult struct {
	findings  map[string][]provider.Finding // keyed by repository name
	rate      int64
	rateKnown bool
}

// collectCodeScanning lists the organization's open code scanning alerts (optionally
// filtered to one SARIF tool) and groups them by repository, mapping each to a
// static_analysis finding. Any API error (including 403/404) is returned so the
// caller records it as a source error; the rate-limit reading is captured whenever a
// response is available.
func (p *Provider) collectCodeScanning(ctx context.Context, org string) (codeScanningResult, error) {
	res := codeScanningResult{findings: make(map[string][]provider.Finding)}

	opts := &gh.AlertListOptions{
		State:    "open",
		ToolName: p.toolName, // empty = all tools
	}
	opts.ListOptions.PerPage = 100

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
			res.findings[repo] = append(res.findings[repo], provider.Finding{
				Severity: provider.NormalizeSeverity(codeScanningSeverity(a)),
				Category: provider.CategoryStaticAnalysis,
			})
		}

		if resp == nil || resp.NextPage == 0 {
			return res, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
	zlog.Warn().Str("org", org).Int("maxPages", p.maxPages).Msg("github code scanning pagination cap reached")
	return res, nil
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
