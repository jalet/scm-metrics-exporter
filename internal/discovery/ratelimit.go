package discovery

import (
	"context"
	"fmt"
	"time"

	gh "github.com/google/go-github/v89/github"
)

// Budget is a credential's remaining API budget, used by the operator to decide whether to
// dispatch collection Jobs. It is provider-neutral: each provider populates it from whatever
// rate-limit signal it exposes.
type Budget struct {
	// Remaining is the tightest remaining request count across the API resources the
	// collection Jobs consume.
	Remaining int
	// Reset is when that tightest resource's window resets.
	Reset time.Time
	// Known reports whether the provider supplied rate-limit information. When false the
	// caller does not gate on the budget (the guard no-ops).
	Known bool
}

// GitHubRateBudget reports the remaining GitHub API budget for the client's credential. It
// queries the /rate_limit endpoint, which does not itself consume quota, and returns the
// tighter of the REST core and GraphQL buckets, since collection Jobs spend from both.
func GitHubRateBudget(ctx context.Context, client *gh.Client) (Budget, error) {
	limits, _, err := client.RateLimit.Get(ctx)
	if err != nil {
		return Budget{}, fmt.Errorf("discovery: github rate limit: %w", err)
	}
	var b Budget
	for _, r := range []*gh.Rate{limits.Core, limits.GraphQL} {
		if r == nil {
			continue
		}
		if !b.Known || r.Remaining < b.Remaining {
			b.Remaining = r.Remaining
			b.Reset = r.Reset.Time
			b.Known = true
		}
	}
	return b, nil
}
