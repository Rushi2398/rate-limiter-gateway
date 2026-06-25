package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestLimiter spins up an in-process miniredis instance and returns a
// Limiter wired to it, plus a cleanup func. miniredis implements the Redis
// protocol (including EVAL/Lua) so our actual bucketScript runs for real —
// this is not a mock of the limiter's behavior, it's the real script
// executing against a real (if in-memory) Redis server.
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

func TestAllow_ExhaustsBucketAtCapacity(t *testing.T) {
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

func TestAllow_RefillsOverTime(t *testing.T) {
	lim, mr := newTestLimiter(t)
	ctx := context.Background()

	capacity, rate := 2, 10 // 10 tokens/sec refill

	// Drain the bucket.
	for i := 0; i < capacity; i++ {
		allowed, _ := lim.Allow(ctx, "client-c", capacity, rate)
		if !allowed {
			t.Fatalf("setup: request %d should have been allowed", i)
		}
	}

	// Immediately retrying should fail — no time has passed.
	allowed, _ := lim.Allow(ctx, "client-c", capacity, rate)
	if allowed {
		t.Error("expected denial immediately after exhausting bucket")
	}

	// Advance miniredis's virtual clock (used for TTL) and also sleep
	// briefly so our script's real time.Now() reflects elapsed time,
	// simulating the refill rate.
	mr.FastForward(200 * time.Millisecond)
	time.Sleep(150 * time.Millisecond) // ~1.5 tokens at 10/sec

	allowed, err := lim.Allow(ctx, "client-c", capacity, rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected request to be allowed after refill window elapsed")
	}
}

func TestAllow_ClientsAreIsolated(t *testing.T) {
	lim, _ := newTestLimiter(t)
	ctx := context.Background()

	capacity := 1

	// Exhaust client-x's single token.
	allowed, _ := lim.Allow(ctx, "client-x", capacity, 1)
	if !allowed {
		t.Fatal("setup: client-x first request should be allowed")
	}
	allowed, _ = lim.Allow(ctx, "client-x", capacity, 1)
	if allowed {
		t.Fatal("setup: client-x should be exhausted")
	}

	// A completely different client must be unaffected.
	allowed, err := lim.Allow(ctx, "client-y", capacity, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected client-y to have its own independent bucket, but it was denied")
	}
}

func TestAllow_RejectsInvalidConfig(t *testing.T) {
	lim, _ := newTestLimiter(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		capacity int
		rate     int
	}{
		{"zero capacity", 0, 5},
		{"negative capacity", -1, 5},
		{"zero rate", 10, 0},
		{"negative rate", 10, -5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lim.Allow(ctx, "client-z", tc.capacity, tc.rate)
			if err == nil {
				t.Errorf("expected error for capacity=%d rate=%d, got nil", tc.capacity, tc.rate)
			}
		})
	}
}

// TestAllow_RefillIsSubSecondPrecise proves that refill is NOT quantized
// to whole-second steps. "rate" is documented as tokens/second as a
// *unit*, but the underlying calculation tracks elapsed time in
// milliseconds — this test inspects the raw Redis hash state directly
// (bypassing Allow's boolean return, which only tells us "allowed or
// not", not the fractional token count) to confirm a sub-second gap
// produces a proportional, non-zero fractional refill.
func TestAllow_RefillIsSubSecondPrecise(t *testing.T) {
	lim, _ := newTestLimiter(t)
	rdb := lim.rdb // white-box access within the package for inspection
	ctx := context.Background()

	capacity, rate := 100, 100 // 100 tok/sec — easy mental math: 1 tok per 10ms

	// Drain most of the bucket down to a known, small token count so we
	// can observe the refill clearly. capacity=100, rate=100: spend 95
	// tokens immediately (elapsed time ~0), leaving ~5.
	for i := 0; i < 95; i++ {
		allowed, err := lim.Allow(ctx, "client-precision", capacity, rate)
		if err != nil || !allowed {
			t.Fatalf("setup request %d: allowed=%v err=%v", i, allowed, err)
		}
	}

	tokensBefore := readTokens(t, ctx, rdb, "client-precision")
	// Loose sanity bound: the 95-call setup loop itself takes real wall
	// time (each call is a round-trip to miniredis), during which the
	// bucket keeps refilling — so tokensBefore won't be exactly 5, just
	// "small and positive". We only care that setup left us with a low,
	// known-ish starting point; the actual assertion is on the *delta*
	// after the controlled sleep below, not on this absolute value.
	if tokensBefore < 0 || tokensBefore > 20 {
		t.Fatalf("tokensBefore = %v, want a small positive value (sanity check on setup)", tokensBefore)
	}

	// Sleep a deliberately sub-second, sub-100ms gap. At rate=100/sec,
	// 30ms should add ~3 tokens (0.030 * 100 = 3.0) — clearly non-zero
	// and clearly not "however many tokens a full second would add".
	const sleepMS = 30
	time.Sleep(sleepMS * time.Millisecond)

	// A throwaway Allow call to trigger the script's refill calculation
	// and update "last", then we inspect the resulting state.
	_, err := lim.Allow(ctx, "client-precision", capacity, rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tokensAfter := readTokens(t, ctx, rdb, "client-precision")

	// tokensAfter = tokensBefore + (sleepMS/1000 * rate) - 1 (the token
	// this last Allow call just spent). Compute the expected refill and
	// assert it's in a tight, sub-second-precision band — if refill were
	// quantized to whole seconds, this would be ~0 (since well under 1s
	// elapsed), not ~3.
	expectedRefill := (float64(sleepMS) / 1000.0) * float64(rate)
	actualRefill := (tokensAfter - tokensBefore) + 1 // +1 to undo this call's own spend

	if actualRefill < expectedRefill*0.5 || actualRefill > expectedRefill*2.0 {
		t.Errorf("actual refill = %.3f tokens, want ~%.3f (sub-second precision check failed — "+
			"got tokensBefore=%.3f tokensAfter=%.3f)", actualRefill, expectedRefill, tokensBefore, tokensAfter)
	}
	if actualRefill < 0.5 {
		t.Errorf("actual refill = %.3f tokens after a %dms sleep — looks like refill is NOT sub-second precise (expected ~%.1f)",
			actualRefill, sleepMS, expectedRefill)
	}

	t.Logf("sub-second precision confirmed: %dms elapsed -> ~%.2f tokens refilled (expected ~%.2f)",
		sleepMS, actualRefill, expectedRefill)
}

// readTokens inspects the raw "tokens" field of a client's bucket hash
// directly via HGET, bypassing the Allow() boolean abstraction so tests
// can assert on the actual fractional token count.
func readTokens(t *testing.T, ctx context.Context, rdb *redis.Client, clientID string) float64 {
	t.Helper()
	val, err := rdb.HGet(ctx, "rl:"+clientID, "tokens").Float64()
	if err != nil {
		t.Fatalf("failed to read tokens for %s: %v", clientID, err)
	}
	return val
}

// TestAllow_ConcurrentRequestsRespectCapacity is the most important test in
// this file: it proves the Lua script's atomicity. If check-and-decrement
// were not atomic, concurrent goroutines could all read "tokens=1" before
// any of them write back the decrement, over-admitting requests — the
// classic race condition that token buckets are supposed to prevent.
func TestAllow_ConcurrentRequestsRespectCapacity(t *testing.T) {
	lim, _ := newTestLimiter(t)
	ctx := context.Background()

	capacity := 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowedCount := 0

	// Fire 200 concurrent requests at a bucket with capacity 50 and a
	// near-zero refill rate so refill doesn't interfere with the count.
	concurrency := 200
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, err := lim.Allow(ctx, "client-concurrent", capacity, 1)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if allowed {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowedCount > capacity {
		t.Errorf("atomicity violated: %d requests allowed against capacity %d", allowedCount, capacity)
	}
	t.Logf("allowed %d/%d concurrent requests against capacity %d", allowedCount, concurrency, capacity)
}
