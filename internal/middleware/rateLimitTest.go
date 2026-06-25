package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rushi2398/rate-limiter-gateway/internal/config"
	"github.com/Rushi2398/rate-limiter-gateway/internal/ratelimit"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestSetup spins up an in-process miniredis instance, a real Limiter on top of it, and a minimal Config — giving each test a clean, isolated rate-limit backend. Using the real Limiter (not a mock) means these tests exercise the actual Lua script through the actual middleware, the same code path that runs in production.
func newTestSetup(t *testing.T, cfg *config.Config) (*ratelimit.Limiter, func(http.Handler) http.Handler) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	lim := ratelimit.New(rdb)
	return lim, RateLimit(lim, cfg)
}

func passThroughHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream reached"))
	})
}

func TestRateLimit_AllowsRequestWithinCapacity(t *testing.T) {
	cfg := &config.Config{DefaultCapacity: 5, DefaultRate: 5, Clients: map[string]config.ClientRule{}}
	_, mw := newTestSetup(t, cfg)

	handler := mw(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "client-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "upstream reached" {
		t.Errorf("body = %q, want request to reach upstream handler", rec.Body.String())
	}
}

func TestRateLimit_BlocksRequestBeyondCapacity(t *testing.T) {
	cfg := &config.Config{DefaultCapacity: 2, DefaultRate: 1, Clients: map[string]config.ClientRule{}}
	_, mw := newTestSetup(t, cfg)
	handler := mw(passThroughHandler())

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "client-2")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Exhaust the 2-token bucket.
	for i := 0; i < 2; i++ {
		rec := makeReq()
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (within capacity)", i, rec.Code)
		}
	}

	// Third request should be denied.
	rec := makeReq()
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestRateLimit_429ResponseHasExpectedHeaders(t *testing.T) {
	cfg := &config.Config{DefaultCapacity: 1, DefaultRate: 1, Clients: map[string]config.ClientRule{}}
	_, mw := newTestSetup(t, cfg)
	handler := mw(passThroughHandler())

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "client-3")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	makeReq() // consume the single token
	rec := makeReq()

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want %q", got, "1")
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining = %q, want %q", got, "0")
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "1" {
		t.Errorf("X-RateLimit-Limit = %q, want %q", got, "1")
	}
}

func TestRateLimit_DifferentClientsHaveIndependentBuckets(t *testing.T) {
	cfg := &config.Config{DefaultCapacity: 1, DefaultRate: 1, Clients: map[string]config.ClientRule{}}
	_, mw := newTestSetup(t, cfg)
	handler := mw(passThroughHandler())

	reqFor := func(client string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", client)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Exhaust client-a's bucket.
	reqFor("client-a")
	blocked := reqFor("client-a")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("client-a second request: status = %d, want 429", blocked.Code)
	}

	// client-b must be unaffected.
	allowed := reqFor("client-b")
	if allowed.Code != http.StatusOK {
		t.Errorf("client-b: status = %d, want 200 (independent bucket)", allowed.Code)
	}
}

func TestRateLimit_UsesClientSpecificRuleOverDefault(t *testing.T) {
	cfg := &config.Config{
		DefaultCapacity: 1,
		DefaultRate:     1,
		Clients: map[string]config.ClientRule{
			"vip-client": {Capacity: 10, Rate: 10},
		},
	}
	_, mw := newTestSetup(t, cfg)
	handler := mw(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "vip-client")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "10" {
		t.Errorf("X-RateLimit-Limit = %q, want %q (vip-client's configured capacity, not the default)", got, "10")
	}
}

func TestRateLimit_FallsBackToRemoteAddrWithoutAPIKey(t *testing.T) {
	cfg := &config.Config{DefaultCapacity: 1, DefaultRate: 1, Clients: map[string]config.ClientRule{}}
	_, mw := newTestSetup(t, cfg)
	handler := mw(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:54321" // no X-API-Key header set
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — unauthenticated request should still get a (shared) rate limit, not be rejected outright", rec.Code)
	}
}

func TestClientIDFromRequest(t *testing.T) {
	t.Run("uses X-API-Key when present", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "my-key")
		req.RemoteAddr = "10.0.0.1:1234"

		if got := clientIDFromRequest(req); got != "my-key" {
			t.Errorf("clientIDFromRequest() = %q, want %q", got, "my-key")
		}
	})

	t.Run("falls back to RemoteAddr when header absent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"

		if got := clientIDFromRequest(req); got != "10.0.0.1:1234" {
			t.Errorf("clientIDFromRequest() = %q, want %q", got, "10.0.0.1:1234")
		}
	})
}

func TestRetryAfterSeconds(t *testing.T) {
	cases := []struct {
		rate int
		want string
	}{
		{rate: 0, want: "1"},
		{rate: -5, want: "1"},
		{rate: 1, want: "1"},
		{rate: 50, want: "1"},
		{rate: 1000, want: "1"},
	}
	for _, tc := range cases {
		if got := retryAfterSeconds(tc.rate); got != tc.want {
			t.Errorf("retryAfterSeconds(%d) = %q, want %q", tc.rate, got, tc.want)
		}
	}
}

// TestRateLimit_FailsOpenByDefaultWhenRedisIsUnreachable is the most important test in this file for production safety: it proves that a Redis outage degrades the gateway to "unlimited" rather than "fully down", which is the documented default when FailOpen is left unset in config.
// Without this behavior, a Redis blip would take down every API behind the gateway — a far worse failure mode than temporarily skipping rate limiting.
func TestRateLimit_FailsOpenByDefaultWhenRedisIsUnreachable(t *testing.T) {
	rdb := unreachableRedisClient(t)
	lim := ratelimit.New(rdb)

	// FailOpen intentionally left nil here — this test exists specifically
	// to prove the *default* (unset) behavior is fail-open.
	cfg := &config.Config{DefaultCapacity: 1, DefaultRate: 1, Clients: map[string]config.ClientRule{}}
	handler := RateLimit(lim, cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "client-during-outage")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — request should fail OPEN by default when redis is unreachable", rec.Code)
	}
	if rec.Body.String() != "upstream reached" {
		t.Errorf("body = %q, want request to reach upstream despite redis outage", rec.Body.String())
	}
}

// TestRateLimit_FailsClosedWhenConfigured proves the opposite policy also works: with fail_open: false, a Redis outage should reject requests with 503 rather than silently letting unmetered traffic through.
// This matters for cases where the rate limit is a security or billing boundary rather than a courtesy control — see config.go's doc comment on FailOpen for the full tradeoff.
func TestRateLimit_FailsClosedWhenConfigured(t *testing.T) {
	rdb := unreachableRedisClient(t)
	lim := ratelimit.New(rdb)

	failOpen := false
	cfg := &config.Config{
		DefaultCapacity: 1,
		DefaultRate:     1,
		FailOpen:        &failOpen,
		Clients:         map[string]config.ClientRule{},
	}
	handler := RateLimit(lim, cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "client-during-outage")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 — request should fail CLOSED when fail_open: false and redis is unreachable", rec.Code)
	}
	if rec.Body.String() == "upstream reached" {
		t.Error("upstream handler was reached despite fail_open: false — request should have been rejected before reaching it")
	}
}

// unreachableRedisClient returns a redis client pointed at a port nothing listens on, so every command fails with a connection error — simulating a Redis outage without relying on timing or external infra.
func unreachableRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1", // port 1 is reserved, nothing binds here
		DialTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { rdb.Close() })
	return rdb
}
