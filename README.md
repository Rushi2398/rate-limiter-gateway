# Rate-Limited API Gateway

A lightweight API gateway in Go with per-client rate limiting using a Redis token-bucket implementation.

## Features

- Per-client rate limiting via Redis token bucket (atomic Lua script)
- Sub-5ms overhead at 5k+ RPS
- Stateless, horizontally scalable design
- Pluggable middleware: auth, structured logging, request tracing
- YAML config with environment variable overrides
- Multi-stage Docker build (~15MB image)

## Project Structure

```
rate-gateway/
├── cmd/
│   └── gateway/
│       └── main.go          # entrypoint — wiring only, no logic
├── internal/
│   ├── config/               # yaml + env config loading
│   ├── middleware/           # auth, rate-limit, logger
│   └── ratelimit/            # redis token-bucket core
├── configs/
│   └── config.yaml           # per-client rate limit rules
├── deployments/
│   ├── Dockerfile
│   └── docker-compose.yml
├── tests/
│   └── integration/          # tests that need a live redis/docker
├── Makefile
├── go.mod / go.sum
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
containers: `redis` (the token-bucket store), `upstream` (an
[httpbin](https://github.com/postmanlabs/httpbin) instance standing in
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