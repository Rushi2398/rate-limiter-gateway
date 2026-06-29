# Rate-Limited API Gateway

A lightweight API gateway in Go with per-client rate limiting using a Redis token-bucket implementation.

## Features

- Per-client rate limiting via Redis token bucket (atomic Lua script)
- Sub-5ms overhead at ~4k+ RPS
- Stateless, horizontally scalable design
- Pluggable middleware: auth, structured logging, request tracing
- YAML config with environment variable overrides
- Multi-stage Docker build (~15MB image)

## Project Structure

```
rate-gateway/
├── cmd/
│   └── gateway/
│       └── main.go          # entrypoint
│   └── upstream/
│       └── main.go 
│       └── handler.go 
│       └── health.go 
│       └── models.go         
├── internal/
│   ├── config/               # yaml + env config loading
│   ├── middleware/           # auth, rate-limit, logger
│   └── ratelimit/            # redis token-bucket core
├── configs/
│   └── config.yaml           # per-client rate limit rules
├── deployments/
│   ├── Dockerfile.gateway
│   ├── Dockerfile.upstream
│   └── docker-compose.yml
├── Makefile
├── go.mod / go.sum            
├── load_test.js              # k6 load test
└── bench_results.txt         # recorded benchmark numbers + how to re-run them
```

`internal/` is private to this module by Go's compiler — nothing outside
`rate-gateway` can import it. `cmd/gateway/main.go` stays thin: it only
wires config, the limiter, and middleware together.

## Quick Start

```bash
make docker-up
curl -H "X-API-Key: api-key-abc123" http://localhost:8080/get
```

`make docker-up` builds the gateway image and brings up three
containers: `redis` (the token-bucket store), `upstream` s instance standing in
for your real backend — swap the `upstream` service in
`deployments/docker-compose.yml` for your actual service in any
non-demo deployment), and `gateway` itself, wired to both via
`REDIS_ADDR` and `UPSTREAM_URL`.

```bash
make docker-down   # tear the stack down
```

## Running locally

```bash
make run
curl http://localhost:8080/health   # → ok
```

## Common tasks

```bash
make build       # compile binary to bin/gateway
make test         # run unit tests with race detector
make docker-up    # start gateway + redis + mock upstream
make docker-down  # tear the stack down
```

## Benchmarking the rate-limit overhead

The "sub-5ms overhead" claim is about the Redis round-trip inside
`Limiter.Allow()` — the actual rate-limiting decision, not the full
HTTP request lifecycle. Two layers of evidence back it, with different
confidence levels:

```bash
# Real Redis round-trip latency (needs a live redis-server):
REAL_REDIS_ADDR=localhost:6379 go test ./internal/ratelimit/... \
  -bench='BenchmarkAllow_RealRedis' -benchtime=3s -run=^$ -benchmem
```

For the full gateway+proxy+network path — the number that actually
backs a "5k+ RPS" claim — run the k6 load test against the real
docker-compose stack:

```bash
make docker-up
k6 run load_test.js
make docker-down
```

Record k6's own p95/p99 `http_req_duration` and achieved
iterations/sec from its summary output.