package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rushi2398/rate-limiter-gateway/internal/config"
)

func cfgWithClients(clients map[string]config.ClientRule, allowAnyonmous *bool) *config.Config {
	return &config.Config{
		DefaultCapacity: 1,
		DefaultRate:     1,
		AllowAnonymous:  allowAnyonmous,
		Clients:         clients,
	}
}

func boolPtr(b bool) *bool { return &b }

func TestAuth_AllowsKnownClient(t *testing.T) {
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {
			Capacity: 10,
			Rate:     10,
		},
	}, nil)
	handler := Auth(cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "known-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a recognized client key", rec.Code)
	}
}

func TestAuth_RejectsUnknownKeyWhenAnonymousDisallowed(t *testing.T) {
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {Capacity: 10, Rate: 10},
	}, boolPtr(false))
	handler := Auth(cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "totally-made-up-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an unrecognized key when allow_anonymous: false", rec.Code)
	}
}

func TestAuth_RejectsMissingKeyWhenAnonymousDisallowed(t *testing.T) {
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {Capacity: 10, Rate: 10},
	}, boolPtr(false))
	handler := Auth(cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil) // no X-API-Key header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a missing key when allow_anonymous: false", rec.Code)
	}
}

func TestAuth_AllowsMissingKeyWhenAnonymousAllowed(t *testing.T) {
	// nil AllowAnonymous = default = true.
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {Capacity: 10, Rate: 10},
	}, nil)
	handler := Auth(cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil) // no X-API-Key header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — missing key should pass through when allow_anonymous defaults true", rec.Code)
	}
}

func TestAuth_AllowsUnknownKeyWhenAnonymousAllowed(t *testing.T) {
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {Capacity: 10, Rate: 10},
	}, boolPtr(true))
	handler := Auth(cfg)(passThroughHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "some-key-not-in-config")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — unrecognized key should still pass through when allow_anonymous: true "+
			"(it'll be rate limited under defaults downstream, not rejected here)", rec.Code)
	}
}

func TestAuth_DistinguishesMissingFromInvalidKeyErrorMessages(t *testing.T) {
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {
			Capacity: 10,
			Rate:     10,
		},
	}, boolPtr(false))

	handler := Auth(cfg)(passThroughHandler())
	t.Run("missing key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Body.String(); !strings.Contains(got, "missing") {
			t.Errorf("body = %q, want a message distinguishing a missing key from an invalid one", got)
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "wrong-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Body.String(); !strings.Contains(got, "invalid") {
			t.Errorf("body = %q, want a message distinguishing an invalid key from a missing one", got)
		}
	})
}

// TestAuthThenRateLimit_ChainedTogether is an integration-style test proving the two middlewares compose correctly in the order they'll actually run in main.go: Auth first, RateLimit second.
// This is the scenario that motivated making AllowAnonymous configurable in the first place — without it, RateLimit's documented RemoteAddr fallback for unauthenticated callers would be unreachable in practice, since Auth would 401 before RateLimit ever saw the request.
func TestAuthThenRateLimit_ChainedTogether(t *testing.T) {
	cfg := cfgWithClients(map[string]config.ClientRule{
		"known-key": {Capacity: 10, Rate: 10},
	}, nil) // allow_anonymous defaults true
	_, rateLimitMW := newTestSetup(t, cfg)

	// Compose: Auth wraps RateLimit wraps the real handler — same order as the production chain.
	handler := Auth(cfg)(rateLimitMW(passThroughHandler()))

	t.Run("anonymous request reaches upstream via RemoteAddr fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil) // no key
		req.RemoteAddr = "198.51.100.5:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 — anonymous request should pass Auth (allow_anonymous default) "+
				"and then be allowed by RateLimit (within capacity)", rec.Code)
		}
	})

	t.Run("known client request reaches upstream", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "known-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 for a known client passing through both middlewares", rec.Code)
		}
	})
}
