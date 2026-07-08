// Package collector polls each configured provider on an interval and caches the
// latest snapshot per provider in memory. The metrics layer only ever reads this
// cache, so a slow or rate-limited provider API call never blocks a Prometheus
// scrape or an OTLP push.
//
// The collector is OpenTelemetry-agnostic: it reports scrape errors through an
// injected callback rather than touching a metric instrument, so it does not import
// the metrics package and a new provider requires no change here.
package collector

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	zlog "github.com/rs/zerolog/log"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// Entry pairs a provider with the target (organization or group) to poll.
type Entry struct {
	Provider provider.Provider
	Target   string
}

// Collector caches the latest Snapshot per provider and runs the poll loop.
type Collector struct {
	entries []Entry // set once in New, never mutated: safe to read without a lock

	mu     sync.RWMutex
	byName map[string]provider.Snapshot
}

// New returns a Collector for the given entries. Provider names
// (Entry.Provider.Name()) are expected to be unique; if two entries share a name
// they share a cache slot and the later poll wins.
func New(entries ...Entry) *Collector {
	return &Collector{
		entries: entries,
		byName:  make(map[string]provider.Snapshot, len(entries)),
	}
}

// ProviderNames returns the configured provider names in entry order.
func (c *Collector) ProviderNames() []string {
	names := make([]string, len(c.entries))
	for i, e := range c.entries {
		names[i] = e.Provider.Name()
	}
	return names
}

// Latest returns the most recent snapshot for a provider, or ok=false if it has not
// been polled yet. The returned snapshot's slices are cloned, so a caller cannot
// mutate cached state.
func (c *Collector) Latest(name string) (provider.Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.byName[name]
	if !ok {
		return provider.Snapshot{}, false
	}
	return cloneSnapshot(snap), true
}

func (c *Collector) store(name string, snap provider.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byName[name] = snap
}

// Run polls every provider once immediately, then on each interval tick, until ctx
// is cancelled. It returns nil on cancellation: signal-driven shutdown is expected,
// not an error. onScrapeError is invoked once per failed source (see pollOne).
func (c *Collector) Run(ctx context.Context, interval time.Duration, onScrapeError func(providerName, source string)) error {
	c.pollAll(ctx, onScrapeError)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.pollAll(ctx, onScrapeError)
		}
	}
}

func (c *Collector) pollAll(ctx context.Context, onScrapeError func(providerName, source string)) {
	for _, e := range c.entries {
		c.pollOne(ctx, e, onScrapeError)
	}
}

// pollOne polls a single provider. A hard error keeps the last cached snapshot and
// records a provider-level scrape error (empty source); a cancellation while
// shutting down is silent. On success, each per-source error in the snapshot is
// recorded and the (possibly partial) snapshot is cached.
func (c *Collector) pollOne(ctx context.Context, e Entry, onScrapeError func(providerName, source string)) {
	name := e.Provider.Name()
	zlog.Debug().Str("provider", name).Str("target", e.Target).Msg("polling provider")

	start := time.Now()
	snap, err := e.Provider.Snapshot(ctx, e.Target)
	elapsed := time.Since(start)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return // shutting down; keep last snapshot, not a fault
		}
		zlog.Warn().Err(err).Str("provider", name).Str("target", e.Target).Dur("elapsed", elapsed).
			Msg("provider snapshot failed; keeping last snapshot")
		onScrapeError(name, "")
		return
	}
	// Per-source failures are logged with their cause inside the provider; here we
	// just feed the scrape-error counter. The summary below records the count.
	for _, se := range snap.SourceErrors {
		onScrapeError(name, se.Source)
	}

	reviewItems, findings := summarize(snap)
	zlog.Info().
		Str("provider", name).
		Str("target", e.Target).
		Int("repos", len(snap.Repos)).
		Int("open_review_items", reviewItems).
		Int("findings", findings).
		Int("source_errors", len(snap.SourceErrors)).
		Dur("elapsed", elapsed).
		Msg("poll complete")
	c.store(name, snap)
}

// summarize totals the open review items and findings across a snapshot's repos for
// the poll-complete log line.
func summarize(s provider.Snapshot) (reviewItems, findings int) {
	for _, r := range s.Repos {
		reviewItems += r.OpenReviewItems
		findings += len(r.Findings)
	}
	return reviewItems, findings
}

// cloneSnapshot deep-copies the slices in s so a caller of Latest cannot mutate
// cached state (defense in depth on top of the provider immutability contract).
func cloneSnapshot(s provider.Snapshot) provider.Snapshot {
	out := provider.Snapshot{
		Repos:        make([]provider.RepoMetrics, len(s.Repos)),
		RateLimits:   slices.Clone(s.RateLimits),
		SourceErrors: slices.Clone(s.SourceErrors),
	}
	for i, r := range s.Repos {
		r.Findings = slices.Clone(r.Findings)
		out.Repos[i] = r
	}
	return out
}
