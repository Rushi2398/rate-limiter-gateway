// BenchmarkAllow measures the cost of a single Allow() call — i.e. one Lua script round-trip to Redis.
// Run with:
//
//	go test ./internal/ratelimit/... -bench=BenchmarkAllow -benchtime=3s -run=^$
//
// Note: miniredis is an in-process pure-Go implementation, so this
// benchmark measures script execution + serialization overhead, not real
// network latency.
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func BenchmarkAllow(b *testing.B) {
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	lim := New(rdb)
	ctx := context.Background()

	// Large capacity/rate so the bucket never actually empties — we're
	// measuring per-call overhead, not testing denial behavior.
	const capacity, rate = 1_000_000, 1_000_000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := lim.Allow(ctx, "bench-client", capacity, rate); err != nil {
			b.Fatalf("Allow failed: %v", err)
		}
	}
}

// BenchmarkAllow_RealRedis is the network-inclusive companion to BenchmarkAllow above: same Lua script, same Limiter, but against an actual redis-server process instead of miniredis's in-process pure-Go implementation.
// This measures single-call latency over a real TCP round-trip — useful context for the "sub-5ms overhead" claim, but it's sequential (b.N iterations run one at a time regardless of GOMAXPROCS), so it says nothing about throughput under concurrent load.
// See BenchmarkAllow_RealRedis_Parallel below for that.

// Skipped automatically unless REAL_REDIS_ADDR is set, since CI environments and most local `go test` runs won't have a Redis server available — this benchmark is opt-in, not part of the default suite.

// Run with a live Redis (e.g. `redis-server --port 6379`):

//	REAL_REDIS_ADDR=localhost:6379 go test ./internal/ratelimit/... \
//	  -bench=BenchmarkAllow_RealRedis -benchtime=3s -run=^$
func BenchmarkAllow_RealRedis(b *testing.B) {
	addr := os.Getenv("REAL_REDIS_ADDR")
	if addr == "" {
		b.Skip("REAL_REDIS_ADDR not set; skipping real-redis benchmark (see doc comment)")
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		b.Fatalf("could not reach redis at %s: %v", addr, err)
	}

	lim := New(rdb)
	ctx := context.Background()

	const capacity, rate = 1_000_000, 1_000_000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := lim.Allow(ctx, "bench-client-real-redis", capacity, rate); err != nil {
			b.Fatalf("Allow failed: %v", err)
		}
	}
}

// BenchmarkAllow_RealRedis_Parallel measures concurrent throughput rather than single-call latency — b.N iterations in a standard Benchmark function run sequentially regardless of GOMAXPROCS, so it can't actually demonstrate anything about RPS under concurrent load.
// This uses b.RunParallel to fire many goroutines at the same Limiter/redis connection pool simultaneously, which is a much closer proxy for what "5k+ RPS" actually means than a sequential per-call number.

// Same env-gating as BenchmarkAllow_RealRedis above.
// Run with:

//	REAL_REDIS_ADDR=localhost:6379 go test ./internal/ratelimit/... \
//	  -bench=BenchmarkAllow_RealRedis_Parallel -benchtime=3s -run=^$
func BenchmarkAllow_RealRedis_Parallel(b *testing.B) {
	addr := os.Getenv("REAL_REDIS_ADDR")
	if addr == "" {
		b.Skip("REAL_REDIS_ADDR not set; skipping real-redis benchmark (see doc comment)")
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		b.Fatalf("could not reach redis at %s: %v", addr, err)
	}

	lim := New(rdb)
	const capacity, rate = 1_000_000, 1_000_000

	b.ResetTimer()
	b.RunParallel(func(p *testing.PB) {
		ctx := context.Background()
		// Each parallel worker uses its own client ID so they don't serialize against each other's Redis key — contending on one shared key would measure Redis's single-key lock contention, not the gateway's per-client concurrent throughput, which is the actual production access pattern (many distinct clients).
		clientID := "bench-parallel-" + randSuffix()
		for p.Next() {
			if _, err := lim.Allow(ctx, clientID, capacity, rate); err != nil {
				b.Fatalf("Allow failed: %v", err)
			}
		}
	})
}

// randSuffix gives each parallel benchmark worker a distinct Redis key without pulling in a UUID dependency just for a benchmark helper.
func randSuffix() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
