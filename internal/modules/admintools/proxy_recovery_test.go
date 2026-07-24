package admintools

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
)

func TestPlanUpstreamRecovery(t *testing.T) {
	// A unit systemd still considers up is mid-boot after a launch and will bind
	// shortly, so the proxy retries behind the "Starting" page.
	for _, status := range []string{"active", "activating", "reloading"} {
		if got := planUpstreamRecovery(status); got != recoveryStarting {
			t.Fatalf("planUpstreamRecovery(%q) = %v, want recoveryStarting", status, got)
		}
	}
	// Any other state — including an empty status when the live probe itself failed
	// — means the container is down and will not self-recover; its launch
	// credentials were scrubbed, so the only path forward is a fresh launch.
	for _, status := range []string{"inactive", "failed", "stopped", "idle", ""} {
		if got := planUpstreamRecovery(status); got != recoveryEnded {
			t.Fatalf("planUpstreamRecovery(%q) = %v, want recoveryEnded", status, got)
		}
	}
}

func TestWriteAdminToolEndedTellsTheUserToRelaunch(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeAdminToolEnded(recorder, admintooloperator.PGAdmin)
	if recorder.Code != 410 {
		t.Fatalf("status = %d, want 410 Gone", recorder.Code)
	}
	// The recovery page must not silently retry a dead upstream — that was the
	// infinite "Starting" spinner this change replaces.
	if recorder.Header().Get("Refresh") != "" {
		t.Fatalf("the ended page must not auto-refresh: %q", recorder.Header().Get("Refresh"))
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "pgAdmin") {
		t.Fatalf("ended page does not name the tool:\n%s", body)
	}
	// The tool opens in its own top-level tab, so a link back to the databases page
	// (where the per-row launch lives) replaces the dead tab with a way to relaunch.
	if !strings.Contains(body, `href="/databases"`) {
		t.Fatalf("ended page has no relaunch link to the databases page:\n%s", body)
	}
}

func TestCreateLaunchAcceptsIdleToolAndMarksItActive(t *testing.T) {
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Second)
	module := newLaunchGatewayModule(t, &clock)
	// The idle-stop timer fired since the last Sync, so the tool is recorded idle —
	// installed but not running. A launch restarts it, so idle must be launchable.
	if _, err := module.database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusIdle).Where("kind = ?", admintooloperator.PGAdmin).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	user := identity.User{ID: "user-1", Username: "admin", Role: "admin"}
	if _, _, err := module.CreateLaunch(ctx, admintooloperator.PGAdmin, LaunchRequest{SourceEngine: "postgresql", DatabaseID: "database-1", AccountID: "account-1"}, user, "127.0.0.1"); err != nil {
		t.Fatalf("launch from idle was rejected: %v", err)
	}
	// The launch bootstrap restarted the container, so the recorded status catches
	// up to active immediately rather than lingering idle until the next Sync.
	tool, err := module.get(ctx, admintooloperator.PGAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Status != string(StatusActive) {
		t.Fatalf("status after launch = %q, want active", tool.Status)
	}
}

func TestRearmActiveToolTimerOnlyOnOnDemandActivity(t *testing.T) {
	operator := &fakeOperator{}
	module := &Module{operator: operator}
	// A renewed session for an on-demand tool re-arms its idle timer.
	module.rearmActiveToolTimer(context.Background(), admintooloperator.PGAdmin, true, true)
	if len(operator.keepAlives) != 1 || operator.keepAlives[0] != admintooloperator.PGAdmin {
		t.Fatalf("keepAlives = %v, want one pgAdmin re-arm", operator.keepAlives)
	}
	// No renewal (traffic within the renew threshold) → no re-arm; and a tool with
	// no idle timer is never re-armed.
	module.rearmActiveToolTimer(context.Background(), admintooloperator.PGAdmin, true, false)
	module.rearmActiveToolTimer(context.Background(), admintooloperator.PHPMyAdmin, false, true)
	if len(operator.keepAlives) != 1 {
		t.Fatalf("keepAlives = %v, want no additional re-arms", operator.keepAlives)
	}
}
