// Package ratelimit implements a distributed token-bucket rate limiter
// backed by Redis. The check-and-decrement operation is performed by a
// single Lua script so that the read, refill, and decrement happen as one
// atomic unit on the Redis server — this is what keeps per-request overhead
// to a single network round-trip instead of multiple GET/SET calls that
// could race under concurrent load.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// bucketScript implements a token bucket:
//   - each client key stores "tokens" (float, tokens remaining) and
//     "last" (unix ms, last refill time)
//   - on each call, we compute how many tokens have accumulated since
//     "last" based on the configured refill rate, cap it at capacity,
//     and then attempt to spend one token
//   - PEXPIRE ensures idle clients' keys are cleaned up automatically
//     instead of growing Redis memory forever
//
// Precision note: "rate" is expressed in tokens/second as a *unit*, not
// as the resolution of the calculation. Time is tracked in unix
// milliseconds (now - last is computed in ms, then divided by 1000 to
// get fractional seconds before multiplying by rate), so refill is
// continuous, not stepped once per second. A client with rate=200
// accumulates ~10 tokens after 50ms, not 0. The only place whole-second
// granularity actually applies is the HTTP Retry-After header returned
// by the middleware — that's a constraint of the HTTP spec itself
// (RFC 7231 defines delay-seconds as an integer), not of this script.
var bucketScript = redis.NewScript(`
local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate     = tonumber(ARGV[2])   -- tokens refilled per second
local now      = tonumber(ARGV[3])   -- current time, unix ms

local state  = redis.call("HMGET", key, "tokens", "last")
local tokens = tonumber(state[1])
local last   = tonumber(state[2])

if tokens == nil then
	tokens = capacity
	last = now
end

local elapsed_sec = math.max(0, now - last) / 1000
tokens = math.min(capacity, tokens + elapsed_sec * rate)

local allowed = 0
if tokens >= 1 then
	tokens = tokens - 1
	allowed = 1
end

redis.call("HMSET", key, "tokens", tokens, "last", now)

-- Expire the key once the bucket would naturally refill to full,
-- plus a small buffer, so idle clients don't leak memory.
local ttl_sec = math.ceil(capacity / rate) + 1
redis.call("PEXPIRE", key, ttl_sec * 1000)

return allowed
`)

// Limiter checks rate limits against Redis using the token-bucket script.
type Limiter struct {
	rdb *redis.Client
}

// New creates a Limiter backed by the given Redis client.
func New(rdb *redis.Client) *Limiter {
	return &Limiter{rdb: rdb}
}

// Allow reports whether a request from clientID should be permitted, given a bucket capacity (max burst size) and refill rate (tokens/sec).
// On Redis error, Allow returns (false, err) — callers decide whether to fail open or closed; see internal/middleware for the fail-open policy used by this gateway.

func (l *Limiter) Allow(ctx context.Context, clientID string, capacity, rate int) (bool, error) {
	if capacity <= 0 || rate <= 0 {
		return false, fmt.Errorf("ratelimit: capacity and rate must be positive, got capacity=%d rate=%d", capacity, rate)
	}

	now := time.Now().UnixMilli()
	key := "rl:" + clientID

	res, err := bucketScript.Run(ctx, l.rdb, []string{key}, capacity, rate, now).Int()
	if err != nil {
		return false, fmt.Errorf("ratelimit: redis script failed: %w", err)
	}

	return res == 1, nil
}
