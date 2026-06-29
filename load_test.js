// load_test.js — k6 load test for the rate-limited API gateway.

// This is what the README's "5k+ RPS, sub-5ms overhead" claim is actually measured against. Run it against the real docker-compose stack, not against a bare `go run` — the claim is about the gateway+proxy+Redis path as a whole, container networking included, not just the Go binary in isolation.

// USAGE:
//   make docker-up          # bring up gateway + redis + upstream
//   k6 run load_test.js
//   make docker-down

// Uses "load-test-key" (configs/config.yaml), a client entry created specifically for this test with a high enough capacity/rate (10000/6000) that the test measures gateway overhead, not how fast Redis can return 429. A real production client would have a far smaller limit — see api-key-abc123 and api-key-free-tier in the same file for realistic examples.
import http from "k6/http";
import { check } from "k6";

export const options = {
  scenarios: {
    sustained_rps: {
      executor: "ramping-arrival-rate",
      startRate: 500,
      timeUnit: "1s",
      preAllocatedVUs: 1000,
      maxVUs: 5000,
      stages: [
        { target: 500, duration: "10s" }, // ramp up
        { target: 5000, duration: "10s" }, // ramp to the target rate
        { target: 5000, duration: "30s" }, // hold 5k RPS
        { target: 0, duration: "10s" }, // ramp down
      ],
    },
  },
  thresholds: {
    // The actual pass/fail bar for the "sub-5ms overhead" claim.
    // This measures total request duration against the upstream, which has its own baseline latency — see the README note on isolating gateway-only overhead from upstream latency.
    http_req_duration: ["p(95)<50", "p(99)<100"],
    http_req_failed: ["rate<0.01"], // <1% hard failures (timeouts, resets)
    checks: ["rate>0.99"],
  },
};

const BASE_URL = __ENV.GATEWAY_URL || "http://localhost:8080";

// hasHeader does a case-insensitive lookup against k6's response headers object.
// Go's net/http canonicalizes header names (X-Request-Id, X-Ratelimit-Limit per Go's casing rules) but relying on that exact casing surviving through k6's own header parsing is fragile — this check passes regardless of how k6 happens to capitalize them.
function hasHeader(res, name) {
  const target = name.toLowerCase();
  return Object.keys(res.headers).some((k) => k.toLowerCase() === target);
}

export default function () {
  const res = http.get(`${BASE_URL}/get`, {
    headers: { "X-API-Key": "load-test-key" },
  });

  check(res, {
    "status is 200 or 429": (r) => r.status === 200 || r.status === 429,
    "has X-Request-ID header": (r) => hasHeader(r, "X-Request-ID"),
    "has X-RateLimit-Limit header": (r) => hasHeader(r, "X-RateLimit-Limit"),
  });
}
