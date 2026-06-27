package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rushi2398/rate-limiter-gateway/internal/config"
	"github.com/Rushi2398/rate-limiter-gateway/internal/ratelimit"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Errorf("body = %q, want %q", got, "ok\n")
	}
}

func TestUpstreamTarget_DefaultsWhenEnvSet(t *testing.T) {
	t.Setenv("UPSTREAM_URL", "")

	u, err := upstreamTarget()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := u.String(); got != "http://upstream:80" {
		t.Errorf("upstreamTarget() = %q, want default %q", got, "http://upstream:80")
	}
}

func TestUpstreamTarget_RespectsEnvOverride(t *testing.T) {
	t.Setenv("UPSTREAM_URL", "http://localhost:9000")

	u, err := upstreamTarget()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := u.String(); got != "http://localhost:9000" {
		t.Errorf("upstreamTarget() = %q, want %q", got, "http://localhost:9000")
	}
}

func TestUpstreamTarget_RejectsMalformedURL(t *testing.T) {
	// A control character makes url.Parse fail outright.
	t.Setenv("UPSTREAM_URL", "http://\x7f-bad-url")

	_, err := upstreamTarget()
	if err == nil {
		t.Error("expected an error for a malformed UPSTREAM_URL, got nil")
	}
}

// TestBuildHandler_FullChainBehavior is an integration-style test proving the assembled chain (Logger -> Auth -> RateLimit -> proxy) behaves correctly end to end at the level main.go actually wires it — this is the same composition exercised live against a real binary, real Redis, and a real upstream during this commit's manual verification, but kept here too so `go test` alone (no docker, no manual curl) catches a regression in the wiring itself.
func TestBuildHandler_FullChainBehavior(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	t.Cleanup(func() { rdb.Close() })

	lim := ratelimit.New(rdb)
	cfg := &config.Config{
		DefaultCapacity: 1,
		DefaultRate:     1,
		Clients: map[string]config.ClientRule{
			"known-key": {Capacity: 5, Rate: 5},
		},
	}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream"))
	})

	handler := buildHandler(upstream, lim, cfg)

	t.Run("known client within capacity reaches upstream", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "known-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != "upstream" {
			t.Errorf("body = %q, want request to reach the upstream handler", rec.Body.String())
		}
	})

	t.Run("unknown client falls back to default capacity and gets rate limited", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "never-seen-this-key-before")
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req)
		if rec1.Code != http.StatusOK {
			t.Fatalf("first request: status = %d, want 200 (within default capacity 1)", rec1.Code)
		}

		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req)
		if rec2.Code != http.StatusTooManyRequests {
			t.Errorf("second request: status = %d, want 429 (default capacity 1 exhausted)", rec2.Code)
		}
	})
}
