package main

// Command gateway is the rate-limited API gateway's entrypoint. It loads config, connects to Redis, builds the middleware chain, and reverse proxies allowed requests to an upstream service.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Rushi2398/rate-limiter-gateway/internal/config"
	"github.com/Rushi2398/rate-limiter-gateway/internal/middleware"
	"github.com/Rushi2398/rate-limiter-gateway/internal/ratelimit"
	"github.com/redis/go-redis/v9"
)

const (
	configPath      = "configs/config.yaml"
	listenAddr      = ":8080"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer rdb.Close()

	// Fail fast at startup if Redis is unreachable, rather than silently discovering it on the first real request via RateLimit's fail-open /fail-closed path.
	// An operator deploying a broken config should see the failure immediately in their deploy logs, not in production traffic metrics an hour later.

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("connecting to redis at %s: %w", cfg.RedisAddr, err)
	}
	slog.Info("connected to redis", "addr", cfg.RedisAddr)

	upstreamURL, err := upstreamTarget()
	if err != nil {
		return err
	}

	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)

	lim := ratelimit.New(rdb)
	handler := buildHandler(proxy, lim, cfg)
	mux := http.NewServeMux()

	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	return serveWithGracefulShutdown(srv)
}

// buildHandler composes the middleware chain around the reverse proxy.
// Order matters here and is documented on each middleware's own doc comment, repeated here for the one place that actually assembles them:

//  1. Logger (outermost) — must see the request first and the response
//     last, so it captures true total latency and the FINAL status code
//     (including 401s and 429s from the layers below, not just whatever
//     the proxy returned).
//  2. Auth — validates the client identity before RateLimit trusts it as
//     a key to rate-limit on.
//  3. RateLimit — enforces the per-client token bucket.
//  4. proxy (innermost) — only requests that survive all three reach the
//     actual upstream.

func buildHandler(proxy http.Handler, lim *ratelimit.Limiter, cfg *config.Config) http.Handler {
	h := proxy
	h = middleware.RateLimit(lim, cfg)(h)
	h = middleware.Auth(cfg)(h)
	h = middleware.Logger(h)
	return h
}

// upstreamTarget reads the upstream URL from UPSTREAM_URL, defaulting to the mock httpbin-style service docker-compose brings up under the name "upstream".
// Kept as an env var rather than a config.yaml field since it's deployment topology (where's the backend), not a rate-limiting policy — the same distinction config.go's doc comments draw between REDIS_ADDR (env) and per-client rules (yaml).
func upstreamTarget() (*url.URL, error) {
	raw := os.Getenv("UPSTREAM_URL")
	if raw == "" {
		raw = "http://upstream:80"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing UPSTREAM_URL %q: %w", raw, err)
	}
	return u, nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

// serveWithGracefulShutdown runs srv until SIGINT/SIGTERM, then gives in-flight requests up to shutdownTimeout to finish before returning.
// Without this, a deploy or container restart would hard-kill requests mid-flight instead of draining them.
func serveWithGracefulShutdown(srv *http.Server) error {
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("gateway listening", "addr", srv.Addr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		return err
	case sig := <-sigCh:
		slog.Info("shutdown signal received, draining in-flight requests", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("shutdown complete")
	return nil
}
