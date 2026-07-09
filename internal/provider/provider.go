// Package provider defines the source-control provider abstraction and the
// provider-neutral domain types the collector and metrics layers consume.
//
// A Provider polls one source-control platform (GitHub, GitLab) for a target (an
// organization or group) and returns a Snapshot. This abstraction is the seam that
// lets a new provider slot in without changing internal/collector or
// internal/metrics: those packages depend only on the types declared here.
//
// Snapshots are immutable after they are returned. A provider builds a fresh
// Snapshot on every poll and never mutates one it has already handed back, which
// lets the collector cache and serve snapshots without copying under its lock.
// Providers are also OpenTelemetry-agnostic: rate-limit readings and per-source
// scrape errors ride on the Snapshot, so the metrics layer never imports a provider
// package.
package provider

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Provider polls a single source-control platform and reports its metrics.
type Provider interface {
	// Name returns a stable identifier for the platform, used as the "provider"
	// metric attribute (for example "github" or "gitlab").
	Name() string

	// Snapshot polls target (an organization for GitHub, a group for GitLab) and
	// returns the current metrics.
	//
	// A returned error signals a total failure with nothing usable (for example the
	// target does not exist or the credentials are unusable). Partial degradation,
	// where one data source fails while others succeed, is reported in
	// Snapshot.SourceErrors with a nil error and whatever data was collected.
	Snapshot(ctx context.Context, target string) (Snapshot, error)
}

// RepoSnapshotter is an optional capability for providers that can collect a single
// repository in isolation. It powers the per-repo collection path (operator-dispatched
// run-once Jobs), where each Job scopes its credentials and API calls to one repository.
//
// It is deliberately separate from Provider so a provider can add the capability without
// forcing every provider to implement it: callers type-assert for it and fall back to
// Snapshot when it is absent.
type RepoSnapshotter interface {
	// SnapshotRepo polls a single repository identified by owner and repo name, returning
	// a Snapshot containing just that repository. Error semantics match Provider.Snapshot:
	// a returned error means nothing usable was collected, while partial degradation is
	// reported in Snapshot.SourceErrors with a nil error.
	SnapshotRepo(ctx context.Context, owner, repo string) (Snapshot, error)
}

// Snapshot is the immutable result of one poll of a Provider.
type Snapshot struct {
	// Repos holds per-repository metrics.
	Repos []RepoMetrics
	// RateLimits holds the remaining API quota per resource, feeding the
	// scm.api.rate_limit_remaining gauge.
	RateLimits []RateLimit
	// SourceErrors lists the data sources that failed during this poll, feeding the
	// scm.exporter.scrape_errors counter. It is empty on a fully successful poll.
	SourceErrors []SourceError
}

// RepoMetrics holds the metrics for a single repository.
type RepoMetrics struct {
	// Name is the repository name (without the owner or organization prefix).
	Name string
	// OpenReviewItems is the number of open review items (pull or merge requests).
	OpenReviewItems int
	// Findings lists the open security findings for the repository.
	Findings []Finding
	// Posture is the repository's security-posture snapshot, or nil when the provider
	// did not capture it (feeds the scm.repo.info gauge). It is treated as immutable.
	Posture *RepoPosture
	// WorkflowRuns tallies recent CI workflow-run outcomes (GitHub Actions) within a
	// lookback window. It is populated only when workflow collection is enabled, and feeds
	// the scm.workflow_runs.recent gauge.
	WorkflowRuns []WorkflowRunStat
	// ResolvedFindings lists findings resolved within the lifecycle resolution window. It
	// is populated only when lifecycle collection is enabled, and feeds the finding-state
	// gauge and (via the store) the remediation histogram.
	ResolvedFindings []ResolvedFinding
}

// WorkflowRunStat is the count of recent CI runs for one workflow and conclusion (for
// example workflow "ci", conclusion "failure", count 3).
type WorkflowRunStat struct {
	// Workflow is the workflow name (GitHub Actions workflow / run name).
	Workflow string
	// Conclusion is the run conclusion, lowercased (success, failure, cancelled, ...).
	Conclusion string
	// Count is the number of runs with this (workflow, conclusion) in the lookback window.
	Count int
}

// WorkflowRunStatsFromTally flattens a workflow -> conclusion -> count tally into a slice
// sorted by workflow then conclusion, for a stable, low-cardinality series set. Providers
// build the tally while paging their CI runs.
func WorkflowRunStatsFromTally(tally map[string]map[string]int) []WorkflowRunStat {
	stats := make([]WorkflowRunStat, 0, len(tally))
	workflows := make([]string, 0, len(tally))
	for w := range tally {
		workflows = append(workflows, w)
	}
	slices.Sort(workflows)
	for _, w := range workflows {
		conclusions := make([]string, 0, len(tally[w]))
		for c := range tally[w] {
			conclusions = append(conclusions, c)
		}
		slices.Sort(conclusions)
		for _, c := range conclusions {
			stats = append(stats, WorkflowRunStat{Workflow: w, Conclusion: c, Count: tally[w][c]})
		}
	}
	return stats
}

// RepoPosture is a repository's security-configuration snapshot. Some fields are
// admin-gated on GitHub, so a token without admin access may report them as false.
type RepoPosture struct {
	// Visibility is public, private, or internal (lowercased).
	Visibility string
	// Archived reports whether the repository is archived.
	Archived bool
	// DependabotEnabled reports whether automated dependency-vulnerability alerting is
	// enabled: GitHub Dependabot alerts, or GitLab dependency scanning.
	DependabotEnabled bool
	// BranchProtected reports whether the default branch has a branch-protection rule.
	BranchProtected bool
}

// Finding is a single open security finding.
type Finding struct {
	// Severity is one of the Severity* constants (see NormalizeSeverity). Values a
	// provider does not recognize pass through lowercased.
	Severity string
	// Category is one of the Category* constants.
	Category string
	// Ecosystem is the dependency package ecosystem (npm, pip, ...) for dependency
	// findings; empty otherwise. Emitted as the optional "ecosystem" metric label only
	// when that dimension is enabled.
	Ecosystem string
	// Tool is the scanning tool that produced the finding (code-scanning tool name,
	// GitLab scanner); empty when unknown. Emitted as the optional "tool" metric label
	// only when that dimension is enabled.
	Tool string
}

// ResolvedFinding is a security finding that has left the open state (fixed or dismissed)
// within the resolution window. State is the coarse lifecycle state (State* constants);
// Resolution is the normalized three-way outcome (Resolution* constants). ID is the
// provider-stable alert identifier used to deduplicate remediation counting.
type ResolvedFinding struct {
	ID         string
	Category   string
	Severity   string
	State      string
	Resolution string
	CreatedAt  time.Time
	ResolvedAt time.Time
}

// RateLimit is the remaining API quota for one resource of a provider.
type RateLimit struct {
	// Resource identifies the API surface, one of the Resource* constants.
	Resource string
	// Remaining is the number of requests left in the current window.
	Remaining int64
}

// SourceError records that a single data source failed during a poll.
type SourceError struct {
	// Source identifies the failed data source, one of the Source* constants.
	Source string
}

// Canonical severities emitted on the "severity" metric attribute.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	// SeverityUnknown labels findings whose source reports no severity -- GitHub
	// secret-scanning alerts carry no severity field.
	SeverityUnknown = "unknown"
)

// Finding categories emitted on the "category" metric attribute.
const (
	CategoryDependency     = "dependency"
	CategoryStaticAnalysis = "static_analysis"
	CategorySecret         = "secret"
	CategoryContainer      = "container"
)

// Data sources, emitted on the "source" attribute of scm.exporter.scrape_errors.
const (
	SourceGraphQL        = "graphql"
	SourceREST           = "rest"
	SourceSecretScanning = "secret_scanning"
	SourceWorkflows      = "workflows"
)

// SourceLifecycle labels scrape errors from the resolved-alert (lifecycle) collection path.
const SourceLifecycle = "lifecycle"

// Normalized remediation outcomes emitted on the "resolution" histogram label.
const (
	ResolutionFixed                 = "fixed"
	ResolutionDismissedNotARisk     = "dismissed_not_a_risk"
	ResolutionDismissedAcceptedRisk = "dismissed_accepted_risk"
)

// Coarse finding lifecycle states emitted on the "state" attribute of scm.findings.by_state.
const (
	StateOpen          = "open"
	StateFixed         = "fixed"
	StateDismissed     = "dismissed"
	StateAutoDismissed = "auto_dismissed"
	StateResolved      = "resolved"
)

// RemediationBucket is one cumulative histogram bucket: LE is the upper bound in seconds
// (math.Inf(1) for the overflow bucket) and Count is the cumulative observation count.
type RemediationBucket struct {
	LE    float64
	Count int64
}

// RemediationSeries is one scope's cumulative remediation histogram, read from the store
// for emission as monotonic counters.
type RemediationSeries struct {
	Provider   string
	Repo       string
	Category   string
	Resolution string
	Buckets    []RemediationBucket
	Sum        float64
	Count      int64
}

// RemediationBucketBounds are the finite histogram upper bounds in seconds (1h..90d). The
// store adds an implicit +Inf overflow bucket. Single source of truth for both the store
// (which fields to increment) and the metrics layer (which le labels to emit).
var RemediationBucketBounds = []float64{3600, 21600, 86400, 259200, 604800, 1209600, 2592000, 7776000}

// remediationScopeSep joins the scope label tuple. The unit separator never appears in a
// repository path or the fixed provider/category/resolution enums, so the join is reversible.
const remediationScopeSep = "\x1f"

// RemediationScope encodes the histogram label tuple into a single scope key used by the
// remediation store.
func RemediationScope(providerName, repo, category, resolution string) string {
	return strings.Join([]string{providerName, repo, category, resolution}, remediationScopeSep)
}

// ParseRemediationScope reverses RemediationScope, returning ok=false for any string that is
// not exactly four separator-joined fields.
func ParseRemediationScope(scope string) (providerName, repo, category, resolution string, ok bool) {
	parts := strings.Split(scope, remediationScopeSep)
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

// API resources, emitted on the "resource" attribute of
// scm.api.rate_limit_remaining.
const (
	ResourceGraphQL = "graphql"
	ResourceREST    = "rest"
)

// NormalizeSeverity maps a provider-specific severity string to a canonical
// severity. Matching is case-insensitive and ignores surrounding whitespace, so
// both GitHub's GraphQL "MODERATE" and REST "medium" map to SeverityMedium.
//
// Recognized inputs return the matching Severity* constant. Any other non-empty
// input is returned lowercased and trimmed (passthrough), so an unexpected severity
// stays visible in metrics instead of being silently rebucketed; an empty input
// returns the empty string. The result is always lowercase, trimmed, and idempotent
// under a second call.
func NormalizeSeverity(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case SeverityCritical:
		return SeverityCritical
	case SeverityHigh:
		return SeverityHigh
	case SeverityMedium, "moderate":
		return SeverityMedium
	case SeverityLow:
		return SeverityLow
	default:
		return v
	}
}
