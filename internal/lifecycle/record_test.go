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
