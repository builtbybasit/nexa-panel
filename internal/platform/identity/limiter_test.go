package identity

import (
	"testing"
	"time"
)

func TestAttemptLimiterBlocksAndResets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAttemptLimiter(2, time.Minute)
	if !limiter.Allow("remote", now) {
		t.Fatal("first attempt should be allowed")
	}
	if !limiter.Allow("remote", now) {
		t.Fatal("second attempt should be allowed")
	}
	if limiter.Allow("remote", now) {
		t.Fatal("attempt above the limit should be blocked")
	}
	if !limiter.Allow("remote", now.Add(time.Minute)) {
		t.Fatal("attempt should be allowed after the window")
	}
	limiter.Reset("remote")
	if !limiter.Allow("remote", now) {
		t.Fatal("attempt should be allowed after reset")
	}
}

func TestAttemptLimiterBoundsAttackerControlledKeys(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAttemptLimiterWithCapacity(2, time.Minute, 2)
	if !limiter.Allow("oldest", now) {
		t.Fatal("oldest key should be allowed")
	}
	if !limiter.Allow("second", now.Add(time.Second)) {
		t.Fatal("second key should be allowed")
	}
	if !limiter.Allow("third", now.Add(2*time.Second)) {
		t.Fatal("new key should be allowed after bounded eviction")
	}
	if got := len(limiter.buckets); got != 2 {
		t.Fatalf("bucket count = %d, want 2", got)
	}
	if _, exists := limiter.buckets["oldest"]; exists {
		t.Fatal("oldest live bucket was not evicted")
	}
}

func TestAttemptLimiterSweepsExpiredKeysBeforeEviction(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAttemptLimiterWithCapacity(2, time.Minute, 2)
	limiter.Allow("expired-a", now)
	limiter.Allow("expired-b", now)
	if !limiter.Allow("fresh", now.Add(time.Minute)) {
		t.Fatal("fresh key should be allowed")
	}
	if got := len(limiter.buckets); got != 1 {
		t.Fatalf("bucket count after sweep = %d, want 1", got)
	}
}
