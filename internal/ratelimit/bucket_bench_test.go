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
