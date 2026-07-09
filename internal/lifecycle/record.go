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
// remediation scope. A finding with a zero or non-positive duration, or an empty id, is
// skipped. A duration longer than the resolution window is also skipped: it cannot be a
// real in-window observation and signals a bad or zero-value timestamp (for example a
// CreatedAt that failed to parse and fell back to the zero time), which would otherwise
// inflate the +Inf overflow bucket as a bogus multi-decade remediation. Store errors are
// joined and returned; a returned error is non-fatal to the caller (recorded as a lifecycle
// source error).
func Record(ctx context.Context, st RecordStore, providerName string, snap provider.Snapshot, window time.Duration) error {
	var errs []error
	for _, repo := range snap.Repos {
		for _, rf := range repo.ResolvedFindings {
			if rf.ID == "" || rf.Resolution == "" {
				continue
			}
			duration := rf.ResolvedAt.Sub(rf.CreatedAt)
			if duration <= 0 || duration > window {
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
