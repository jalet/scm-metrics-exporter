package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

type call struct {
	scope, id string
	duration  time.Duration
}

type fakeStore struct{ calls []call }

func (f *fakeStore) RecordIfNew(_ context.Context, scope, id string, duration, _ time.Duration) (bool, error) {
	f.calls = append(f.calls, call{scope, id, duration})
	return true, nil
}

func TestRecordEmitsScopedDurations(t *testing.T) {
	snap := provider.Snapshot{Repos: []provider.RepoMetrics{{
		Name: "acme/svc",
		ResolvedFindings: []provider.ResolvedFinding{{
			ID: "da-1", Category: provider.CategoryDependency, Resolution: provider.ResolutionFixed,
			CreatedAt: time.Unix(1000, 0), ResolvedAt: time.Unix(1000+7200, 0),
		}},
	}}}
	fs := &fakeStore{}
	if err := Record(context.Background(), fs, "github", snap, 90*24*time.Hour); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(fs.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(fs.calls))
	}
	got := fs.calls[0]
	wantScope := provider.RemediationScope("github", "acme/svc", provider.CategoryDependency, provider.ResolutionFixed)
	if got.scope != wantScope || got.id != "da-1" || got.duration != 2*time.Hour {
		t.Fatalf("call = %+v, want scope=%q id=da-1 duration=2h", got, wantScope)
	}
}

// TestRecordSkipsBogusCreatedAt guards against a finding whose CreatedAt failed to parse
// (a provider's time-parse fallback to the zero time, e.g. GitLab's parseGitLabTime) from
// inflating the +Inf overflow bucket as a multi-decade "remediation". A zero CreatedAt
// yields a duration far exceeding any real resolution window, so it must be skipped just
// like the existing zero/negative-duration guard.
func TestRecordSkipsBogusCreatedAt(t *testing.T) {
	window := 90 * 24 * time.Hour
	snap := provider.Snapshot{Repos: []provider.RepoMetrics{{
		Name: "acme/svc",
		ResolvedFindings: []provider.ResolvedFinding{
			{
				ID: "bogus-1", Category: provider.CategoryDependency, Resolution: provider.ResolutionFixed,
				CreatedAt: time.Time{}, ResolvedAt: time.Unix(1000, 0),
			},
			{
				ID: "good-1", Category: provider.CategoryDependency, Resolution: provider.ResolutionFixed,
				CreatedAt: time.Unix(1000, 0), ResolvedAt: time.Unix(1000+7200, 0),
			},
		},
	}}}
	fs := &fakeStore{}
	if err := Record(context.Background(), fs, "github", snap, window); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(fs.calls) != 1 {
		t.Fatalf("calls=%d, want 1 (bogus CreatedAt finding must be skipped)", len(fs.calls))
	}
	if fs.calls[0].id != "good-1" {
		t.Fatalf("calls[0].id = %q, want good-1", fs.calls[0].id)
	}
}
