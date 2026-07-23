package httpapi

import (
	"strconv"
	"testing"
	"time"
)

func TestIPThrottleFixedWindow(t *testing.T) {
	start := time.Unix(0, 0)
	throttle := NewIPThrottle(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !throttle.Allow("198.51.100.7", start) {
			t.Fatalf("request %d within budget was rejected", i)
		}
	}
	if throttle.Allow("198.51.100.7", start) {
		t.Fatal("fourth request in window was allowed past the limit")
	}

	// A different client keeps its own independent budget.
	if !throttle.Allow("203.0.113.9", start) {
		t.Fatal("a distinct client was throttled by another client's usage")
	}

	// Once the window rolls over the original client is allowed again.
	if !throttle.Allow("198.51.100.7", start.Add(time.Minute)) {
		t.Fatal("budget did not reset after the window elapsed")
	}
}

func TestIPThrottleUnlimitedNeverRejects(t *testing.T) {
	now := time.Unix(0, 0)
	for _, throttle := range []*IPThrottle{NewIPThrottle(0, time.Minute), NewIPThrottle(5, 0)} {
		for i := 0; i < 100; i++ {
			if !throttle.Allow("198.51.100.7", now) {
				t.Fatal("an unlimited throttle rejected a request")
			}
		}
	}
}

func TestIPThrottleEvictsToStayBounded(t *testing.T) {
	now := time.Unix(0, 0)
	throttle := NewIPThrottleWithCapacity(1, time.Minute, 2)

	// Fill capacity with two live clients, each at its limit.
	throttle.Allow("a", now)
	throttle.Allow("b", now)

	// A third distinct client forces eviction rather than unbounded growth.
	throttle.Allow("c", now)
	throttle.mu.Lock()
	buckets := len(throttle.buckets)
	throttle.mu.Unlock()
	if buckets > 2 {
		t.Fatalf("bucket map grew to %d, want <= 2", buckets)
	}

	// Expired buckets are reclaimed before a live one is evicted.
	fresh := NewIPThrottleWithCapacity(1, time.Minute, 2)
	fresh.Allow("old-1", now)
	fresh.Allow("old-2", now)
	later := now.Add(2 * time.Minute)
	for i := 0; i < 2; i++ {
		fresh.Allow(strconv.Itoa(i), later)
	}
	fresh.mu.Lock()
	freshBuckets := len(fresh.buckets)
	fresh.mu.Unlock()
	if freshBuckets > 2 {
		t.Fatalf("bucket map grew to %d after expiry, want <= 2", freshBuckets)
	}
}
