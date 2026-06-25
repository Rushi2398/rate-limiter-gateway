package middleware

import (
	"net/http"

	"github.com/Rushi2398/rate-limiter-gateway/internal/config"
)

// Auth returns middleware that validates X-API-Key against the configured client list.
// It must run BEFORE RateLimit in the chain:
// once a key passes here, RateLimit can trust clientIDFromRequest's value instead of letting any caller claim an arbitrary client ID (and either dodge their own limit or burn someone else's quota).

// The validity check itself is intentionally simple — "is this key one of the clients listed in config" — rather than real credential validation (hashing, JWT, OAuth, etc).
// That's a deliberate scope boundary for this commit: the valuable part is the middleware boundary itself, so a real auth backend can be swapped in later (changing only this file) without touching RateLimit, Logger, or the proxy.

// Behavior on a missing or unrecognized key is controlled by cfg.AllowAnonymousOrDefault():

//	true  (default) — let the request through unauthenticated. It will reach RateLimit with no client ID, which falls back to RemoteAddr — a coarse, shared limit for anonymous traffic, similar to a public API's free tier. Appropriate when some endpoints (docs, health checks, public reads) shouldn't require a key at all.
//	false           — reject with 401 before the request reachesRateLimit or the upstream at all. Appropriate when every request must be attributable to a known client — e.g. the API is not meant to have a public/anonymous surface.

// This mirrors the FailOpen pattern in config.go: a security/access policy expressed as data the operator controls, not an opinion baked into the code.

func Auth(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(clientIDHeader)
			_, known := cfg.Clients[key]
			if key != "" && known {
				next.ServeHTTP(w, r)
				return
			}
			if cfg.AllowAnonymousOrDefault() {
				next.ServeHTTP(w, r)
				return
			}
			if key == "" {
				http.Error(w, "missing X-API-Key", http.StatusUnauthorized)
				return
			}
			http.Error(w, "invalid API key", http.StatusUnauthorized)
		})
	}
}
