// newTestLimiter spins up an in-process miniredis instance and returns a Limiter wired to it, plus a cleanup func. miniredis implements the Redis protocol (including EVAL/Lua) so our actual bucketScript runs for real.
package ratelimit

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLimiter(t *testing.T) (*Limiter, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	return New(rdb), mr
}

func TestAllow_FirstRequestSucceeds(t *testing.T) {
	lim, _ := newTestLimiter(t)
	ctx := context.Background()

	allowed, err := lim.Allow(ctx, "client-a", 10, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected first request to be allowed, got denied")
	}
}

func TestAllow_ExhaustsBucketCapacity(t *testing.T) {
	lim, _ := newTestLimiter(t)
	ctx := context.Background()

	capacity := 5
	for i := 0; i < capacity; i++ {
		allowed, err := lim.Allow(ctx, "client-b", capacity, 1)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !allowed {
			t.Fatalf("request %d: expected allowed (within capacity %d), got denied", i, capacity)
		}
	}

	// One more request beyond capacity should be denied.
	allowed, err := lim.Allow(ctx, "client-b", capacity, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected request beyond capacity to be denied, got allowed")
	}
}
