package collector

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

func TestMain(m *testing.M) {
	zerolog.SetGlobalLevel(zerolog.Disabled) // silence the poll-failure Warn in tests
	os.Exit(m.Run())
}

type fakeProvider struct {
	name  string
	snap  provider.Snapshot
	err   error
	block bool // block until ctx is cancelled, then return ctx.Err()
	calls atomic.Int32
}

var _ provider.Provider = (*fakeProvider)(nil)

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Snapshot(ctx context.Context, _ string) (provider.Snapshot, error) {
	f.calls.Add(1)
	if f.block {
		<-ctx.Done()
		return provider.Snapshot{}, ctx.Err()
	}
	if f.err != nil {
		return provider.Snapshot{}, f.err
	}
	return f.snap, nil
}

// fakeRepoProvider also implements provider.RepoSnapshotter, recording the owner/repo it
// was asked to collect. The embedded fakeProvider.Snapshot must not be called in repo mode.
type fakeRepoProvider struct {
	fakeProvider
	repoSnap provider.Snapshot
	repoErr  error
	gotOwner string
	gotRepo  string
}

var _ provider.RepoSnapshotter = (*fakeRepoProvider)(nil)

func (f *fakeRepoProvider) SnapshotRepo(_ context.Context, owner, repo string) (provider.Snapshot, error) {
	f.gotOwner, f.gotRepo = owner, repo
	if f.repoErr != nil {
		return provider.Snapshot{}, f.repoErr
	}
	return f.repoSnap, nil
}

func TestPollOnceRepoScoped(t *testing.T) {
	p := &fakeRepoProvider{
		fakeProvider: fakeProvider{name: "github"},
		repoSnap:     provider.Snapshot{Repos: []provider.RepoMetrics{{Name: "widget", OpenReviewItems: 2}}},
	}
	c := New(Entry{Provider: p, Target: "acme", Repo: "widget"})
	if err := c.PollOnce(context.Background(), (&errRec{}).record); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if p.gotOwner != "acme" || p.gotRepo != "widget" {
		t.Errorf("SnapshotRepo(%q, %q), want (acme, widget)", p.gotOwner, p.gotRepo)
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("full Snapshot called %d times, want 0 (repo-scoped)", n)
	}
	got, ok := c.Latest("github")
	if !ok || len(got.Repos) != 1 || got.Repos[0].Name != "widget" {
		t.Fatalf("Latest = %+v ok=%v, want the widget snapshot stored", got, ok)
	}
}

func TestPollOnceHardErrorReturnsError(t *testing.T) {
	p := &fakeProvider{name: "github", err: errors.New("boom")}
	c := New(Entry{Provider: p, Target: "org"})
	rec := &errRec{}
	if err := c.PollOnce(context.Background(), rec.record); err == nil {
		t.Fatal("PollOnce: got nil, want the hard error surfaced for the exit code")
	}
	if got := rec.snapshot(); len(got) != 1 || got[0] != [2]string{"github", ""} {
		t.Errorf("scrape errors = %v, want one provider-level (empty-source) error", got)
	}
}

func TestPollOnceRepoUnsupportedProvider(t *testing.T) {
	// fakeProvider does not implement RepoSnapshotter, so a repo-scoped entry must error.
	c := New(Entry{Provider: &fakeProvider{name: "gitlab"}, Target: "grp", Repo: "widget"})
	if err := c.PollOnce(context.Background(), (&errRec{}).record); err == nil {
		t.Fatal("PollOnce: got nil, want an error for a provider without single-repo support")
	}
}

// errRec is a concurrency-safe record of onScrapeError calls.
type errRec struct {
	mu  sync.Mutex
	got [][2]string
}

func (r *errRec) record(providerName, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, [2]string{providerName, source})
}

func (r *errRec) snapshot() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

func waitFor(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func TestLatestIsolatesCallerFromCache(t *testing.T) {
	c := New(Entry{Provider: &fakeProvider{name: "github"}, Target: "org"})

	if _, ok := c.Latest("github"); ok {
		t.Error("Latest ok = true before first poll, want false")
	}
	if got := c.ProviderNames(); !slices.Equal(got, []string{"github"}) {
		t.Errorf("ProviderNames = %v, want [github]", got)
	}

	c.store("github", provider.Snapshot{
		Repos: []provider.RepoMetrics{{
			Name:            "a",
			OpenReviewItems: 1,
			Findings:        []provider.Finding{{Severity: "high", Category: "dependency"}},
		}},
	})

	got, ok := c.Latest("github")
	if !ok {
		t.Fatal("Latest ok = false after store, want true")
	}
	// Mutating the returned snapshot must not corrupt the cache.
	got.Repos[0].Name = "mutated"
	got.Repos[0].Findings[0].Severity = "low"

	again, _ := c.Latest("github")
	if again.Repos[0].Name != "a" || again.Repos[0].Findings[0].Severity != "high" {
		t.Errorf("cache mutated through returned snapshot: %+v", again.Repos[0])
	}
}

func TestRunPollsImmediatelyThenReturnsNilOnCancel(t *testing.T) {
	p := &fakeProvider{name: "github", snap: provider.Snapshot{Repos: []provider.RepoMetrics{{Name: "r"}}}}
	c := New(Entry{Provider: p, Target: "org"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, time.Hour, func(string, string) {}) }()

	waitFor(t, "immediate poll to populate cache", func() bool {
		_, ok := c.Latest("github")
		return ok
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if p.calls.Load() < 1 {
		t.Error("provider was not polled")
	}
}

func TestRunHardErrorRecordsAndKeepsCache(t *testing.T) {
	p := &fakeProvider{name: "github", err: errors.New("boom")}
	c := New(Entry{Provider: p, Target: "org"})
	rec := &errRec{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx, time.Hour, rec.record) }()

	waitFor(t, "hard error to be recorded", func() bool { return len(rec.snapshot()) >= 1 })

	if got := rec.snapshot()[0]; got != [2]string{"github", ""} {
		t.Errorf("scrape error = %v, want {github, \"\"} (provider-level, no source)", got)
	}
	if _, ok := c.Latest("github"); ok {
		t.Error("Latest ok = true after hard error, want false (no snapshot to cache)")
	}
}

func TestRunPartialErrorRecordsSourcesAndStoresSnapshot(t *testing.T) {
	p := &fakeProvider{
		name: "github",
		snap: provider.Snapshot{
			Repos:        []provider.RepoMetrics{{Name: "r", OpenReviewItems: 2}},
			SourceErrors: []provider.SourceError{{Source: provider.SourceGraphQL}, {Source: provider.SourceREST}},
		},
	}
	c := New(Entry{Provider: p, Target: "org"})
	rec := &errRec{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx, time.Hour, rec.record) }()

	waitFor(t, "partial snapshot to be cached", func() bool {
		_, ok := c.Latest("github")
		return ok
	})

	got := rec.snapshot()
	if !slices.Contains(got, [2]string{"github", provider.SourceGraphQL}) ||
		!slices.Contains(got, [2]string{"github", provider.SourceREST}) {
		t.Errorf("scrape errors = %v, want both graphql and rest", got)
	}

	cached, _ := c.Latest("github")
	if len(cached.Repos) != 1 || cached.Repos[0].OpenReviewItems != 2 {
		t.Errorf("partial snapshot not stored: %+v", cached)
	}
}

func TestRunReturnsNilOnCancelDuringSlowPoll(t *testing.T) {
	p := &fakeProvider{name: "github", block: true}
	c := New(Entry{Provider: p, Target: "org"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	onErr := func(string, string) { t.Error("cancellation during a slow poll must not record a scrape error") }
	go func() { done <- c.Run(ctx, time.Hour, onErr) }()

	waitFor(t, "slow poll to start", func() bool { return p.calls.Load() >= 1 })
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel during slow poll")
	}
}
