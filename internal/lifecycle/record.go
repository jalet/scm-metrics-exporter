// Package lifecycle records resolved findings from a snapshot into the resolution store,
// translating each into a scoped, deduplicated remediation observation. It is provider- and
// storage-neutral: it depends only on the provider domain types and a minimal store port.
package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// RecordStore is the subset of the resolution store the record pass needs.
type RecordStore interface {
	RecordIfNew(ctx context.Context, scope, id string, duration, window time.Duration) (bool, error)
}

// Record walks a snapshot's resolved findings and records each into the store under its
// remediation scope. A finding with an empty id, a zero CreatedAt, or a non-positive
// duration is skipped. The zero-CreatedAt guard targets a timestamp that failed to parse and
// fell back to the zero time (for example GitLab's parser on a malformed detectedAt), which
// would otherwise land a bogus multi-decade "remediation" in the +Inf bucket. A duration
// that legitimately exceeds the resolution window is kept: a long-lived finding created well
// before the window but resolved within it is a real, slow remediation and belongs in the
// +Inf bucket. Store errors are joined and returned; a returned error is non-fatal to the
// caller (recorded as a lifecycle source error).
func Record(ctx context.Context, st RecordStore, providerName string, snap provider.Snapshot, window time.Duration) error {
	var errs []error
	for _, repo := range snap.Repos {
		for _, rf := range repo.ResolvedFindings {
			if rf.ID == "" || rf.Resolution == "" || rf.CreatedAt.IsZero() {
				continue
			}
			duration := rf.ResolvedAt.Sub(rf.CreatedAt)
			if duration <= 0 {
				continue
			}
			scope := provider.RemediationScope(providerName, repo.Name, rf.Category, rf.Resolution)
			if _, err := st.RecordIfNew(ctx, scope, rf.ID, duration, window); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
