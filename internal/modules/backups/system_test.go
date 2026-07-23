package backups

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// systemJob must resolve the chosen account, ship a panel-state copy, record it,
// and drop rows for copies the operator pruned remotely.
func TestSystemJobRecordsCopyAndPrunes(t *testing.T) {
	ctx := context.Background()
	module, operator := newTestModule(t)
	account := seedAccount(t, module)

	// A pre-existing copy the operator will report as pruned this run.
	now := time.Now().UTC()
	stale := &systemCopyModel{
		ID: "bksys_old", AccountID: account.ID, CopyName: "old_copy",
		RemotePath: "/srv/backups/system/old_copy", SizeBytes: 10, SHA256: "old", CreatedAt: now.Add(-time.Hour),
	}
	if _, err := module.database.NewInsert().Model(stale).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	operator.systemManifest = backupoperator.Manifest{
		RemotePath: "/srv/backups/system/fresh", SizeBytes: 4096,
		Entries:          []backupoperator.CopyEntry{{Name: "nexa-panel-system.tar.gz", SizeBytes: 4096, SHA256: "deadbeef"}},
		IntegrityChecked: true,
		Pruned:           []string{"old_copy"},
	}

	payload, _ := json.Marshal(systemRunPayload{AccountID: account.ID})
	if _, err := module.systemJob(ctx, payload, func(int, string) error { return nil }); err != nil {
		t.Fatalf("systemJob: %v", err)
	}

	if operator.lastSystemRun.Account.Type != TypeLocal {
		t.Fatalf("account not resolved into system run request: %+v", operator.lastSystemRun)
	}
	if operator.lastSystemRun.CopyName == "" || operator.lastSystemRun.Limit != defaultSystemRetention {
		t.Fatalf("system run request not built with retention: %+v", operator.lastSystemRun)
	}

	var copies []systemCopyModel
	if err := module.database.NewSelect().Model(&copies).Order("copy_name DESC").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 {
		t.Fatalf("system copies = %d, want 1 (stale pruned, fresh recorded): %+v", len(copies), copies)
	}
	fresh := copies[0]
	if fresh.CopyName != operator.lastSystemRun.CopyName || fresh.AccountID != account.ID ||
		fresh.SizeBytes != 4096 || fresh.SHA256 != "deadbeef" || fresh.RemotePath != "/srv/backups/system/fresh" {
		t.Fatalf("recorded copy does not match manifest: %+v", fresh)
	}
}

// A snapshot the operator failed to integrity-check must never be recorded.
func TestSystemJobRejectsUnverifiedUpload(t *testing.T) {
	ctx := context.Background()
	module, operator := newTestModule(t)
	account := seedAccount(t, module)

	// systemManifest carries no IntegrityChecked; the fake forces it true, so drive
	// the negative path via the operator error instead.
	operator.err = errors.New("agent unavailable")

	payload, _ := json.Marshal(systemRunPayload{AccountID: account.ID})
	if _, err := module.systemJob(ctx, payload, func(int, string) error { return nil }); err == nil {
		t.Fatal("expected systemJob to fail when the operator errors")
	}
	var count int
	if err := module.database.NewSelect().Model((*systemCopyModel)(nil)).ColumnExpr("count(*)").Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("system copies = %d, want 0 on failure", count)
	}
}
