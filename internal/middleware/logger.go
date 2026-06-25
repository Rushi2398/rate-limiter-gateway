package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// requestIDHeader is set on the response so a client (or a downstream proxy/log aggregator) can correlate a request across systems using the same ID this gateway logged it under.

const requestIDHeader = "X-Request-ID"

// statusRecorder wraps http.ResponseWriter to capture the status code that was actually written, since the standard interface doesn't expose it after the fact and Logger needs it for the log line written after the handler returns.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(status int) {
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

// Logger returns middleware that emits one structured (slog/JSON) log line per request, after the request completes, with method, path, status, latency, client ID, and a per-request correlation ID.

// Placement in the chain: Logger should wrap the OUTERMOST layer (run first on the way in, last on the way out) so it captures the true total latency and the final status code — including 401s from Auth and 429s from RateLimit, not just what the upstream proxy returned.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := newRequestID()
		w.Header().Set(requestIDHeader, reqID)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		slog.Info("request",
			"request_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"client", r.Header.Get(clientIDHeader),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// newRequestID generates a short random hex ID for log correlation. This intentionally isn't a UUID library dependency — 8 random bytes (16 hex chars) gives a collision probability low enough for log correlation within a single gateway's traffic, without adding a dependency for something stdlib's crypto/rand already covers.

// On the vanishingly rare chance rand.Read itself fails (the docs note it practically never does on any supported platform), we fall back to a fixed placeholder rather than panicking — a missing-but-distinct request ID is a minor logging inconvenience, not a reason to fail a request that otherwise would have succeeded.
func newRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "unknown-request-id"
	}
	return hex.EncodeToString(buf)
}
