package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

func newTestStore(t *testing.T) (*Valkey, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	v, err := NewValkey(Options{Addr: mr.Addr()})
	if err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v, mr
}

func TestRecordIfNewDedups(t *testing.T) {
	v, _ := newTestStore(t)
	ctx := context.Background()
	scope := provider.RemediationScope("github", "acme/svc", provider.CategoryDependency, provider.ResolutionFixed)

	first, err := v.RecordIfNew(ctx, scope, "alert-1", 2*time.Hour, 90*24*time.Hour)
	if err != nil || !first {
		t.Fatalf("first record: counted=%v err=%v, want true/nil", first, err)
	}
	second, err := v.RecordIfNew(ctx, scope, "alert-1", 2*time.Hour, 90*24*time.Hour)
	if err != nil || second {
		t.Fatalf("duplicate record: counted=%v err=%v, want false/nil", second, err)
	}
}

func TestRemediationCumulativeBuckets(t *testing.T) {
	v, _ := newTestStore(t)
	ctx := context.Background()
	scope := provider.RemediationScope("github", "acme/svc", provider.CategoryDependency, provider.ResolutionFixed)

	// 2h resolution: counts in every bucket with le >= 7200 (i.e. all but the 1h bucket).
	if _, err := v.RecordIfNew(ctx, scope, "a1", 2*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// 40d resolution: counts only in the 90d and +Inf buckets.
	if _, err := v.RecordIfNew(ctx, scope, "a2", 40*24*time.Hour, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	series, err := v.Remediation(ctx)
	if err != nil {
		t.Fatalf("Remediation: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("series=%d, want 1", len(series))
	}
	s := series[0]
	if s.Provider != "github" || s.Repo != "acme/svc" || s.Category != provider.CategoryDependency || s.Resolution != provider.ResolutionFixed {
		t.Fatalf("labels wrong: %+v", s)
	}
	if s.Count != 2 {
		t.Fatalf("count=%d, want 2", s.Count)
	}
	if math.Abs(s.Sum-(2*time.Hour+40*24*time.Hour).Seconds()) > 1 {
		t.Fatalf("sum=%v, want %v", s.Sum, (2*time.Hour + 40*24*time.Hour).Seconds())
	}
	want := map[float64]int64{
		3600: 0, 21600: 1, 86400: 1, 259200: 1, 604800: 1, 1209600: 1, 2592000: 1, 7776000: 2,
	}
	got := map[float64]int64{}
	var infCount int64 = -1
	for _, b := range s.Buckets {
		if math.IsInf(b.LE, 1) {
			infCount = b.Count
			continue
		}
		got[b.LE] = b.Count
	}
	for le, c := range want {
		if got[le] != c {
			t.Errorf("bucket le=%v: got %d want %d", le, got[le], c)
		}
	}
	if infCount != 2 {
		t.Errorf("+Inf bucket: got %d want 2", infCount)
	}
}
