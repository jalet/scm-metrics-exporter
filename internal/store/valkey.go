package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/jalet/scm-metrics-exporter/internal/provider"
)

// Options configures the Valkey connection. Addr is host:port; Password is optional.
type Options struct {
	Addr     string
	Password string
}

// Valkey is a ResolutionStore backed by a Valkey/Redis server.
type Valkey struct {
	client *redis.Client
}

// NewValkey dials Valkey and verifies connectivity with PING.
func NewValkey(opts Options) (*Valkey, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("store: Valkey Addr is required")
	}
	c := redis.NewClient(&redis.Options{Addr: opts.Addr, Password: opts.Password})
	if err := c.Ping(context.Background()).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("store: connect Valkey %q: %w", opts.Addr, err)
	}
	return &Valkey{client: c}, nil
}

// Close releases the connection pool.
func (v *Valkey) Close() error { return v.client.Close() }

// RecordIfNew adds id to the scope's dedup set; if newly added it increments every bucket
// with le >= duration (plus the +Inf overflow), the sum, and the count, and registers the
// scope in the index. The dedup set's TTL is refreshed to window on every call so ids age
// out with the query window. All writes run in one pipeline.
func (v *Valkey) RecordIfNew(ctx context.Context, scope, id string, duration, window time.Duration) (bool, error) {
	added, err := v.client.SAdd(ctx, countedKey(scope), id).Result()
	if err != nil {
		return false, fmt.Errorf("store: SADD %s: %w", scope, err)
	}
	// Refresh the dedup-set TTL regardless, so an active scope keeps its window.
	if err := v.client.Expire(ctx, countedKey(scope), window).Err(); err != nil {
		return false, fmt.Errorf("store: EXPIRE %s: %w", scope, err)
	}
	if added == 0 {
		return false, nil // already counted
	}

	secs := duration.Seconds()
	pipe := v.client.Pipeline()
	hk := histKey(scope)
	for _, le := range provider.RemediationBucketBounds {
		if secs <= le {
			pipe.HIncrBy(ctx, hk, bucketField(le), 1)
		}
	}
	pipe.HIncrBy(ctx, hk, bucketField(math.Inf(1)), 1)
	pipe.HIncrByFloat(ctx, hk, "sum", secs)
	pipe.HIncrBy(ctx, hk, "count", 1)
	pipe.SAdd(ctx, histIndexKey, hk)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("store: increment %s: %w", scope, err)
	}
	return true, nil
}

// Remediation reads every tracked scope's cumulative histogram for emission.
func (v *Valkey) Remediation(ctx context.Context) ([]provider.RemediationSeries, error) {
	keys, err := v.client.SMembers(ctx, histIndexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("store: SMEMBERS %s: %w", histIndexKey, err)
	}
	sort.Strings(keys) // deterministic emission order
	out := make([]provider.RemediationSeries, 0, len(keys))
	for _, hk := range keys {
		fields, err := v.client.HGetAll(ctx, hk).Result()
		if err != nil {
			return nil, fmt.Errorf("store: HGETALL %s: %w", hk, err)
		}
		if len(fields) == 0 {
			continue
		}
		scope := hk[len("hist:"):]
		p, repo, cat, res, ok := provider.ParseRemediationScope(scope)
		if !ok {
			continue // skip an unparseable key rather than fail the whole read
		}
		s := provider.RemediationSeries{Provider: p, Repo: repo, Category: cat, Resolution: res}
		for _, le := range provider.RemediationBucketBounds {
			s.Buckets = append(s.Buckets, provider.RemediationBucket{LE: le, Count: parseInt(fields[bucketField(le)])})
		}
		s.Buckets = append(s.Buckets, provider.RemediationBucket{LE: math.Inf(1), Count: parseInt(fields[bucketField(math.Inf(1))])})
		s.Sum = parseFloat(fields["sum"])
		s.Count = parseInt(fields["count"])
		out = append(out, s)
	}
	return out, nil
}

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

var _ ResolutionStore = (*Valkey)(nil)
