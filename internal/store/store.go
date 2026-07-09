// Package store persists remediation-histogram state in Valkey (RESP protocol). It
// deduplicates resolved findings by id so each remediation is counted exactly once, and
// maintains cumulative histogram counters that the metrics layer reads for emission. It
// depends only on the neutral provider domain types.
package store

import (
	"context"
	"math"
	"strconv"
	"time"
)

// ResolutionStore records resolved findings for remediation-histogram accounting.
type ResolutionStore interface {
	// RecordIfNew counts one resolved finding against scope if its id has not been counted
	// before. duration is resolvedAt-createdAt; window sets the dedup-set TTL. It returns
	// whether this call counted the finding.
	RecordIfNew(ctx context.Context, scope, id string, duration, window time.Duration) (bool, error)
}

// key builders. countedKey and histKey share the scope suffix so a scope's dedup set and
// histogram stay aligned. histIndexKey lists every hist key for enumeration on read.
func countedKey(scope string) string { return "counted:" + scope }
func histKey(scope string) string    { return "hist:" + scope }

const histIndexKey = "histkeys"

// bucketField is the hash field name for a finite bucket bound (le:3600) or the overflow
// bucket (le:+Inf).
func bucketField(le float64) string {
	if math.IsInf(le, 1) {
		return "le:+Inf"
	}
	return "le:" + strconv.FormatInt(int64(le), 10)
}
