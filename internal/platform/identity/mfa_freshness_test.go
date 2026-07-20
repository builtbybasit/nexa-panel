package identity

import (
	"context"
	"testing"
	"time"
)

func TestRecentMFAEnforcesSensitiveActionWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	verifiedAt := now.Add(-5 * time.Minute)
	ctx := context.WithValue(context.Background(), principalContextKey{}, principal{
		User: User{ID: "user-1", Role: "admin"}, MFAVerifiedAt: &verifiedAt,
	})
	if !RecentMFA(ctx, now, 10*time.Minute) {
		t.Fatal("five-minute-old verification should satisfy a ten-minute window")
	}
	if RecentMFA(ctx, now, 4*time.Minute) {
		t.Fatal("verification older than the configured window should be rejected")
	}
	if RecentMFA(context.Background(), now, 10*time.Minute) {
		t.Fatal("context without an authenticated principal should be rejected")
	}
}

func TestRecentMFARejectsFutureVerificationTimestamp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	verifiedAt := now.Add(time.Minute)
	ctx := context.WithValue(context.Background(), principalContextKey{}, principal{MFAVerifiedAt: &verifiedAt})
	if RecentMFA(ctx, now, 10*time.Minute) {
		t.Fatal("future verification timestamp should be rejected")
	}
}
