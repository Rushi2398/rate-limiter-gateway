package middleware

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/Rushi2398/rate-limiter-gateway/internal/config"
	"github.com/Rushi2398/rate-limiter-gateway/internal/ratelimit"
)

// Package middleware contains the gateway's HTTP middleware chain: rate limiting, auth, and structured logging.
// Each middleware is an independent func(http.Handler) http.Handler so they can be composed, reordered, or swapped without touching the others.

// clientIDHeader is the header clients use to identify themselves.
// In a production deployment this would typically be set by an upstream auth layer after validating a token, rather than trusted directly from the client — but for the rate limiter itself, all that matters is having a stable per-client identifier to key the bucket on.
const clientIDHeader = "X-API-Key"

// RateLimit returns middleware that enforces per-client token-bucket
// limits using lim, with capacity/rate rules sourced from cfg.

// Fail-open policy: if Redis is unreachable or the script call errors,
// the request is allowed through rather than blocked. This is a
// deliberate availability-over-strictness tradeoff — a Redis outage
// should degrade the gateway to "unlimited" rather than "fully down".
// If your use case requires fail-closed behavior (e.g. for billing
// enforcement), invert the err check below.
func RateLimit(lim *ratelimit.Limiter, cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientID := clientIDFromRequest(r)
			rule := cfg.RuleFor(clientID)

			allow, err := lim.Allow(r.Context(), clientID, rule.Capacity, rule.Rate)
			if err != nil {
				if cfg.FailOpenOrDefault() {
					slog.Warn("ratelimit check failed, failing open",
						"client", clientID,
						"error", err,
					)
					next.ServeHTTP(w, r)
					return
				}
				slog.Error("ratelimit check failed, failing closed",
					"client", clientID,
					"error", err,
				)
				http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
				return
			}

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rule.Capacity))

			if !allow {
				// A token-bucket doesn't track a literal "remaining" counter between calls in a way that's meaningful to expose here (the script's internal state is fractional tokens, not a monotonic remaining count), so we report 0 — the contract callers care about is "you are currently rate limited", not a precise remaining quota.
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("Retry-After", retryAfterSeconds(rule.Rate))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIDFromRequest extracts a stable per-client identifier. Falls back to RemoteAddr so unauthenticated callers still get a (shared, coarse) rate limit instead of bypassing limiting entirely.
func clientIDFromRequest(r *http.Request) string {
	if key := r.Header.Get(clientIDHeader); key != "" {
		return key
	}
	return r.RemoteAddr
}

// retryAfterSeconds gives clients a hint for how long to back off before retrying.
// At a refill rate of `rate` tokens/sec, a new token becomes available after roughly 1/rate seconds. We round up to a whole number of seconds (Retry-After's unit) and enforce a minimum of 1.

// Note: for any rate >= 1 tok/sec (the realistic range — see config.go's validation, which already rejects rate <= 0), 1/rate <= 1, so this always resolves to exactly "1". That's intentional, not a missed edge case: Retry-After has whole-second granularity, so sub-second precision wouldn't be honored by HTTP clients anyway.
// The minimum-1 floor exists only to protect against a future caller passing rate=0 directly, bypassing config validation.

func retryAfterSeconds(rate int) string {
	if rate <= 0 {
		return "1"
	}
	secondsPerToken := 1.0 / float64(rate)
	seconds := max(int(math.Ceil(secondsPerToken)), 1)
	return strconv.Itoa(seconds)
}
