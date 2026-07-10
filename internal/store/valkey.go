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
	Addr string
	// Password is read from the VALKEY_PASSWORD env var / a Secret key, never hardcoded.
	Password string //nolint:gosec // config field name, not a hardcoded credential (G101/G117 false positive)
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

// recordScript atomically dedups id against the scope's counted-set and, only if newly
// added, increments every requested bucket field, the sum, and the count, and registers the
// hist key in the index. Running this as a single Lua script avoids a lost-update window
// where the dedup SADD succeeds but a later, separate round trip incrementing the histogram
// fails: without atomicity that leaves the id permanently marked "counted" with its
// contribution silently dropped, since a retry would see added==0 and never recover it.
//
// KEYS[1] = counted:<scope>, KEYS[2] = hist:<scope>, KEYS[3] = histkeys index
// ARGV[1] = id, ARGV[2] = window in whole seconds, ARGV[3] = duration in seconds,
// ARGV[4:] = bucket field names to increment by 1 (finite buckets with le >= duration, plus
// the +Inf overflow field).
var recordScript = redis.NewScript(`
local added = redis.call('SADD', KEYS[1], ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[2])
if added == 0 then
	return 0
end
for i = 4, #ARGV do
	redis.call('HINCRBY', KEYS[2], ARGV[i], 1)
end
redis.call('HINCRBYFLOAT', KEYS[2], 'sum', ARGV[3])
redis.call('HINCRBY', KEYS[2], 'count', 1)
redis.call('SADD', KEYS[3], KEYS[2])
return 1
`)

// RecordIfNew adds id to the scope's dedup set; if newly added it increments every bucket
// with le >= duration (plus the +Inf overflow), the sum, and the count, and registers the
// scope in the index. The dedup set's TTL is refreshed to window on every call so ids age
// out with the query window. The dedup, TTL refresh, and all increments run atomically in a
// single Lua script so a failure partway through can never leave an id marked "counted"
// without its histogram contribution applied.
func (v *Valkey) RecordIfNew(ctx context.Context, scope, id string, duration, window time.Duration) (bool, error) {
	secs := duration.Seconds()

	fields := make([]string, 0, len(provider.RemediationBucketBounds)+1)
	for _, le := range provider.RemediationBucketBounds {
		if secs <= le {
			fields = append(fields, bucketField(le))
		}
	}
	fields = append(fields, bucketField(math.Inf(1)))

	keys := []string{countedKey(scope), histKey(scope), histIndexKey}
	args := make([]interface{}, 0, 3+len(fields))
	args = append(args, id, int(window.Seconds()), secs)
	for _, f := range fields {
		args = append(args, f)
	}

	res, err := recordScript.Run(ctx, v.client, keys, args...).Int64()
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", scope, err)
	}
	return res == 1, nil
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
		p, repo, cat, res, sev, ok := provider.ParseRemediationScope(scope)
		if !ok {
			continue // skip an unparseable key rather than fail the whole read
		}
		s := provider.RemediationSeries{Provider: p, Repo: repo, Category: cat, Resolution: res, Severity: sev}
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
