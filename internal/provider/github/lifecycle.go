package github

import (
	"context"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v89/github"
	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// collectResolvedFindings gathers a repository's findings resolved within the resolution
// window across code scanning, secret scanning, and Dependabot. A source that is disabled
// or inaccessible (403/404) contributes nothing rather than failing; any other API error is
// returned so the caller records a lifecycle source error.
func (p *Provider) collectResolvedFindings(ctx context.Context, owner, repo string) ([]provider.ResolvedFinding, error) {
	since := time.Now().Add(-p.resolutionWindow)
	var out []provider.ResolvedFinding

	cs, err := p.resolvedCodeScanning(ctx, owner, repo, since)
	if err != nil {
		return nil, err
	}
	out = append(out, cs...)

	ss, err := p.resolvedSecretScanning(ctx, owner, repo, since)
	if err != nil {
		return nil, err
	}
	out = append(out, ss...)

	da, err := p.resolvedDependabot(ctx, owner, repo, since)
	if err != nil {
		return nil, err
	}
	out = append(out, da...)

	zlog.Debug().Str("provider", "github").Str("source", provider.SourceLifecycle).Str("repo", owner+"/"+repo).
		Int("resolved", len(out)).Msg("resolved findings collected")
	return out, nil
}

// resolvedCodeScanning lists fixed and dismissed code-scanning alerts newer than since.
func (p *Provider) resolvedCodeScanning(ctx context.Context, owner, repo string, since time.Time) ([]provider.ResolvedFinding, error) {
	var out []provider.ResolvedFinding
	for _, state := range []string{"fixed", "dismissed"} {
		opts := &gh.AlertListOptions{State: state, ToolName: p.toolName}
		opts.ListOptions.PerPage = 100
		for page := 0; page < p.maxPages; page++ {
			alerts, resp, err := p.rest.CodeScanning.ListAlertsForRepo(ctx, owner, repo, opts)
			if err != nil {
				if notAccessible(err) {
					return out, nil
				}
				return nil, err
			}
			for _, a := range alerts {
				resolvedAt := codeScanningResolvedAt(a)
				if resolvedAt.Before(since) {
					continue
				}
				out = append(out, provider.ResolvedFinding{
					ID:         "cs-" + strconvI64(int64(a.GetNumber())),
					Category:   provider.CategoryStaticAnalysis,
					Severity:   provider.NormalizeSeverity(codeScanningSeverity(a)),
					State:      lifecycleState(state),
					Resolution: codeScanningResolution(state, a.GetDismissedReason()),
					CreatedAt:  a.GetCreatedAt().Time,
					ResolvedAt: resolvedAt,
				})
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.ListOptions.Page = resp.NextPage
		}
	}
	return out, nil
}

func codeScanningResolvedAt(a *gh.Alert) time.Time {
	if !a.GetFixedAt().Time.IsZero() {
		return a.GetFixedAt().Time
	}
	return a.GetDismissedAt().Time
}

// resolvedSecretScanning lists resolved secret-scanning alerts newer than since.
func (p *Provider) resolvedSecretScanning(ctx context.Context, owner, repo string, since time.Time) ([]provider.ResolvedFinding, error) {
	var out []provider.ResolvedFinding
	opts := &gh.SecretScanningAlertListOptions{State: "resolved"}
	opts.ListOptions.PerPage = 100
	for page := 0; page < p.maxPages; page++ {
		alerts, resp, err := p.rest.SecretScanning.ListAlertsForRepo(ctx, owner, repo, opts)
		if err != nil {
			if notAccessible(err) {
				return out, nil
			}
			return nil, err
		}
		for _, a := range alerts {
			resolvedAt := a.GetResolvedAt().Time
			if resolvedAt.Before(since) {
				continue
			}
			out = append(out, provider.ResolvedFinding{
				ID:         "ss-" + strconvI64(int64(a.GetNumber())),
				Category:   provider.CategorySecret,
				Severity:   provider.SeverityUnknown,
				State:      provider.StateResolved,
				Resolution: secretScanningResolution(a.GetResolution()),
				CreatedAt:  a.GetCreatedAt().Time,
				ResolvedAt: resolvedAt,
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return out, nil
}

// resolvedDependabot lists fixed and dismissed (incl. auto-dismissed) Dependabot alerts
// newer than since via REST (open Dependabot alerts stay on the GraphQL path).
func (p *Provider) resolvedDependabot(ctx context.Context, owner, repo string, since time.Time) ([]provider.ResolvedFinding, error) {
	var out []provider.ResolvedFinding
	for _, state := range []string{"fixed", "dismissed", "auto_dismissed"} {
		opts := &gh.ListAlertsOptions{State: gh.Ptr(state)}
		opts.ListOptions.PerPage = 100
		for page := 0; page < p.maxPages; page++ {
			alerts, resp, err := p.rest.Dependabot.ListRepoAlerts(ctx, owner, repo, opts)
			if err != nil {
				if notAccessible(err) {
					return out, nil
				}
				return nil, err
			}
			for _, a := range alerts {
				resolvedAt := dependabotResolvedAt(a)
				if resolvedAt.Before(since) {
					continue
				}
				out = append(out, provider.ResolvedFinding{
					ID:         "da-" + strconvI64(int64(a.GetNumber())),
					Category:   provider.CategoryDependency,
					Severity:   provider.NormalizeSeverity(dependabotSeverity(a)),
					State:      lifecycleState(state),
					Resolution: dependabotResolution(state, a.GetDismissedReason()),
					CreatedAt:  a.GetCreatedAt().Time,
					ResolvedAt: resolvedAt,
				})
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.ListOptions.Page = resp.NextPage
		}
	}
	return out, nil
}

func dependabotResolvedAt(a *gh.DependabotAlert) time.Time {
	if !a.GetFixedAt().Time.IsZero() {
		return a.GetFixedAt().Time
	}
	if !a.GetAutoDismissedAt().Time.IsZero() {
		return a.GetAutoDismissedAt().Time
	}
	return a.GetDismissedAt().Time
}

func dependabotSeverity(a *gh.DependabotAlert) string {
	if a.SecurityAdvisory != nil {
		return a.SecurityAdvisory.GetSeverity()
	}
	return ""
}

// lifecycleState maps a REST alert state string to a provider State* constant.
func lifecycleState(state string) string {
	switch state {
	case "fixed":
		return provider.StateFixed
	case "auto_dismissed":
		return provider.StateAutoDismissed
	case "dismissed":
		return provider.StateDismissed
	default:
		return provider.StateResolved
	}
}

// codeScanningResolution maps a code-scanning (state, dismissedReason) to a resolution.
func codeScanningResolution(state, reason string) string {
	if state == "fixed" {
		return provider.ResolutionFixed
	}
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "false positive", "used in tests":
		return provider.ResolutionDismissedNotARisk
	default: // "won't fix" and anything unexpected
		return provider.ResolutionDismissedAcceptedRisk
	}
}

// secretScanningResolution maps a secret-scanning resolution reason to a resolution.
func secretScanningResolution(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "revoked":
		return provider.ResolutionFixed
	case "false_positive", "used_in_tests", "pattern_deleted":
		return provider.ResolutionDismissedNotARisk
	default: // "wont_fix" and anything unexpected
		return provider.ResolutionDismissedAcceptedRisk
	}
}

// dependabotResolution maps a Dependabot (state, dismissReason) to a resolution.
func dependabotResolution(state, reason string) string {
	if state == "fixed" {
		return provider.ResolutionFixed
	}
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "inaccurate", "not_used":
		return provider.ResolutionDismissedNotARisk
	default: // tolerable_risk, no_bandwidth, fix_started, auto-dismissed, unexpected
		return provider.ResolutionDismissedAcceptedRisk
	}
}

func strconvI64(n int64) string { return strconv.FormatInt(n, 10) }
