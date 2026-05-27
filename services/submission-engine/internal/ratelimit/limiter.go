// Package ratelimit provides a rolling-window rate limiter for the Submission
// Engine backed by a Redis sorted set.
//
// Each contestant's upload history is stored as a sorted set keyed by
// rate_limit:{contestant_id}. Both the score and the member value are the
// upload timestamp in nanoseconds, which allows cheap range eviction and
// deterministic oldest-entry lookup.
//
// The check-and-record operation is executed atomically via a Lua script to
// prevent race conditions under concurrent upload requests from the same
// contestant.
package ratelimit

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// MaxUploads is the maximum number of uploads allowed per rolling window.
	MaxUploads = 5
	// WindowSecs is the size of the rolling window in seconds (60 minutes).
	WindowSecs = 3600
)

// rateLimitScript atomically:
//  1. Evicts entries older than the rolling window start.
//  2. Counts remaining entries.
//  3. If count >= limit: returns {0, oldest_score} so the caller can compute
//     Retry-After.
//  4. Otherwise: records the new upload timestamp and sets the key TTL.
//     Returns {1, 0}.
//
// KEYS[1]  = rate_limit:{contestant_id}
// ARGV[1]  = window_start  (unix nanoseconds as decimal string)
// ARGV[2]  = now           (unix nanoseconds as decimal string, used as score)
// ARGV[3]  = limit         (integer as decimal string)
// ARGV[4]  = member        (unique string identifier for this upload entry)
var rateLimitScript = redis.NewScript(`
local key          = KEYS[1]
local window_start = ARGV[1]
local now          = ARGV[2]
local limit        = tonumber(ARGV[3])
local member       = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, '0', window_start)
local count = redis.call('ZCARD', key)
if count >= limit then
  local oldest = redis.call('ZRANGE', key, '0', '0', 'WITHSCORES')
  return {0, oldest[2]}
end
redis.call('ZADD', key, now, member)
redis.call('EXPIRE', key, 3600)
return {1, 0}
`)

// Limiter enforces a per-contestant rolling-window upload rate limit using a
// Redis sorted set and an atomic Lua script.
type Limiter struct {
	rdb redis.Cmdable
}

// New creates a Limiter backed by the provided Redis client or Cmdable.
func New(rdb redis.Cmdable) *Limiter {
	return &Limiter{rdb: rdb}
}

// key returns the Redis key for a given contestant.
func key(contestantID string) string {
	return fmt.Sprintf("rate_limit:%s", contestantID)
}

// Allow atomically checks whether contestantID is permitted to make another
// upload and, if so, records the upload in the same operation.
//
// Returns:
//   - (true, 0, nil)              — upload is allowed and has been recorded.
//   - (false, retryAfter, nil)    — rate limited; retryAfter is the number of
//     seconds until the oldest upload falls outside the 60-minute window.
//   - (false, 0, err)             — a Redis error occurred.
func (l *Limiter) Allow(ctx context.Context, contestantID string) (allowed bool, retryAfter int64, err error) {
	now := time.Now().UnixNano()
	// window_start is the inclusive lower bound we keep; entries with score <=
	// (now - WindowSecs*1e9 - 1) are stale.
	windowStart := now - WindowSecs*int64(time.Second)

	keys := []string{key(contestantID)}
	// Generate a unique member for this upload entry so that concurrent calls
	// at the same nanosecond don't overwrite each other in the sorted set.
	member := strconv.FormatInt(now, 10) + ":" + strconv.FormatInt(rand.Int63(), 10)
	args := []interface{}{
		strconv.FormatInt(windowStart-1, 10), // prune everything strictly older
		strconv.FormatInt(now, 10),
		strconv.FormatInt(MaxUploads, 10),
		member,
	}

	raw, err := rateLimitScript.Run(ctx, l.rdb, keys, args...).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit: lua script: %w", err)
	}

	if len(raw) < 2 {
		return false, 0, fmt.Errorf("ratelimit: unexpected script result length %d", len(raw))
	}

	// The Lua script returns integers; convert each element to its string
	// representation so we can parse them uniformly.
	toStr := func(v interface{}) string {
		switch val := v.(type) {
		case int64:
			return strconv.FormatInt(val, 10)
		case string:
			return val
		default:
			return fmt.Sprintf("%v", val)
		}
	}

	res0 := toStr(raw[0])
	res1 := toStr(raw[1])

	allowed = res0 == "1"
	if allowed {
		return true, 0, nil
	}

	// Rate limited: res1 is the oldest entry's score (nanoseconds).
	oldestNs, parseErr := strconv.ParseInt(res1, 10, 64)
	if parseErr != nil {
		// Treat a parse failure as a generic internal error rather than
		// masking it as "allowed".
		return false, 0, fmt.Errorf("ratelimit: parse oldest score %q: %w", res1, parseErr)
	}

	// Retry-After = ceil((oldest_ns + window_ns - now_ns) / 1e9)
	remaining := oldestNs + WindowSecs*int64(time.Second) - now
	retryAfter = int64(math.Ceil(float64(remaining) / float64(time.Second)))
	if retryAfter < 1 {
		retryAfter = 1
	}

	return false, retryAfter, nil
}
