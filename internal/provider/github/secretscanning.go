package github

import (
	"context"

	gh "github.com/google/go-github/v89/github"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

type secretScanningResult struct {
	findings  map[string][]provider.Finding // keyed by repository name
	rate      int64
	rateKnown bool
}

// collectSecretScanning lists open secret-scanning alerts grouped by repository, mapping
// each to a secret finding. GitHub secret-scanning alerts carry no severity, so they are
// emitted with SeverityUnknown. Org targets use the org-scoped endpoint; user targets have
// none, so they iterate the user's repositories (tolerating repos without secret scanning
// enabled). Any hard API error is returned so the caller records a source error.
func (p *Provider) collectSecretScanning(ctx context.Context, target string) (secretScanningResult, error) {
	if p.targetType == targetUser {
		return p.collectSecretScanningForUser(ctx, target)
	}
	return p.collectSecretScanningForOrg(ctx, target)
}

func (p *Provider) collectSecretScanningForOrg(ctx context.Context, org string) (secretScanningResult, error) {
	res := secretScanningResult{findings: make(map[string][]provider.Finding)}

	opts := &gh.SecretScanningAlertListOptions{State: "open"}
	opts.ListOptions.PerPage = 100

	total := 0
	for page := 0; page < p.maxPages; page++ {
		alerts, resp, err := p.rest.SecretScanning.ListAlertsForOrg(ctx, org, opts)
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
			res.findings[repo] = append(res.findings[repo], secretFinding())
		}
		total += len(alerts)
		zlog.Debug().Str("provider", "github").Str("source", "secret_scanning").Str("owner", org).
			Int("page", page).Int("alerts_in_page", len(alerts)).Int64("rate_remaining", res.rate).
			Msg("fetched secret scanning page")

		if resp == nil || resp.NextPage == 0 {
			zlog.Debug().Str("provider", "github").Str("source", "secret_scanning").Str("owner", org).
				Int("alerts", total).Int("repos_with_findings", len(res.findings)).Int("pages", page+1).
				Msg("secret scanning collection complete")
			return res, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
	zlog.Warn().Str("owner", org).Int("maxPages", p.maxPages).Msg("github secret scanning pagination cap reached")
	return res, nil
}

func (p *Provider) collectSecretScanningForUser(ctx context.Context, user string) (secretScanningResult, error) {
	res := secretScanningResult{findings: make(map[string][]provider.Finding)}

	repos, rate, ok, err := p.listUserRepos(ctx, user)
	if ok {
		res.rate, res.rateKnown = rate, true
	}
	if err != nil {
		return res, err
	}

	skipped := 0
	for _, repo := range repos {
		findings, rate, ok, accessible, err := p.secretScanningForRepo(ctx, user, repo)
		if ok {
			res.rate, res.rateKnown = rate, true
		}
		if err != nil {
			return res, err
		}
		if !accessible {
			skipped++
			continue
		}
		if len(findings) > 0 {
			res.findings[repo] = append(res.findings[repo], findings...)
		}
	}
	zlog.Debug().Str("provider", "github").Str("source", "secret_scanning").Str("owner", user).
		Int("repos", len(repos)).Int("repos_skipped", skipped).Int("repos_with_findings", len(res.findings)).
		Msg("per-repo secret scanning complete (user target)")
	return res, nil
}

// secretScanningForRepo lists one repository's open secret-scanning alerts. accessible is
// false when secret scanning is disabled or inaccessible on the repo (403/404), treated by
// callers as "no findings" rather than a source failure. Any other API error is returned.
func (p *Provider) secretScanningForRepo(ctx context.Context, owner, repo string) (findings []provider.Finding, rate int64, rateKnown, accessible bool, err error) {
	opts := &gh.SecretScanningAlertListOptions{State: "open"}
	opts.ListOptions.PerPage = 100
	for page := 0; page < p.maxPages; page++ {
		alerts, resp, e := p.rest.SecretScanning.ListAlertsForRepo(ctx, owner, repo, opts)
		if resp != nil {
			rate, rateKnown = int64(resp.Rate.Remaining), true
		}
		if e != nil {
			if notAccessible(e) {
				return findings, rate, rateKnown, false, nil
			}
			return findings, rate, rateKnown, false, e
		}
		for range alerts {
			findings = append(findings, secretFinding())
		}
		if resp == nil || resp.NextPage == 0 {
			return findings, rate, rateKnown, true, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return findings, rate, rateKnown, true, nil
}

// secretFinding is the finding for one open secret-scanning alert. GitHub reports no
// severity for these, so the severity is SeverityUnknown.
func secretFinding() provider.Finding {
	return provider.Finding{Severity: provider.SeverityUnknown, Category: provider.CategorySecret}
}
