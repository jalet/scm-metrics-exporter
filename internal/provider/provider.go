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
	"strings"
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
}

// Finding is a single open security finding.
type Finding struct {
	// Severity is one of the Severity* constants (see NormalizeSeverity). Values a
	// provider does not recognize pass through lowercased.
	Severity string
	// Category is one of the Category* constants.
	Category string
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
	SourceGraphQL = "graphql"
	SourceREST    = "rest"
)

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
