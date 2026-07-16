package identity

import (
	"testing"
	"time"
)

func TestAttemptLimiterBlocksAndResets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAttemptLimiter(2, time.Minute)
	if !limiter.Allow("remote", now) || !limiter.Allow("remote", now) {
		t.Fatal("initial attempts should be allowed")
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
