package audit

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

func newChainModule(t *testing.T) (*Module, *bun.DB) {
	t.Helper()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	log, err := New(context.Background(), database)
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}
	return log, database
}

func recordChain(t *testing.T, log *Module, count int) {
	t.Helper()
	actor := "user_admin"
	for index := 0; index < count; index++ {
		entry := Entry{
			ActorUserID: &actor, Action: "firewall.rule_allowed", Subject: "firewall-rule:8080/tcp",
			RemoteAddress: "203.0.113.7", Metadata: map[string]any{"port": "8080", "sequence": index},
		}
		if err := log.Record(context.Background(), entry); err != nil {
			t.Fatalf("Record returned an error: %v", err)
		}
	}
}

func TestVerifyAcceptsAnIntactChain(t *testing.T) {
	log, _ := newChainModule(t)
	recordChain(t, log, 5)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if !result.Intact || result.Checked != 5 || result.FirstBrokenID != nil {
		t.Fatalf("unexpected verify result: %+v", result)
	}
}

func TestVerifyDetectsAnEditedRow(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 4)

	if _, err := database.NewUpdate().Model((*eventModel)(nil)).
		Set("subject = ?", "firewall-rule:22/tcp").Where("id = ?", 2).Exec(context.Background()); err != nil {
		t.Fatalf("tamper with row: %v", err)
	}

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.FirstBrokenID == nil || *result.FirstBrokenID != 2 {
		t.Fatalf("edited row was not reported: %+v", result)
	}
}

func TestVerifyDetectsADeletedRow(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 4)

	if _, err := database.NewDelete().Model((*eventModel)(nil)).Where("id = ?", 2).Exec(context.Background()); err != nil {
		t.Fatalf("delete row: %v", err)
	}

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.FirstBrokenID == nil || *result.FirstBrokenID != 3 {
		t.Fatalf("deleted row was not reported: %+v", result)
	}
}

// insertPreChainRows simulates a database upgraded from before the chain
// shipped: unhashed rows, with the chain start recorded behind them the way the
// migration records it.
func insertPreChainRows(t *testing.T, database *bun.DB, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		legacy := &eventModel{
			OccurredAt: time.Now().UTC(), Action: "identity.login", Subject: "user:admin", Metadata: "{}",
		}
		if _, err := database.NewInsert().Model(legacy).Exec(context.Background()); err != nil {
			t.Fatalf("insert pre-chain row: %v", err)
		}
	}
	if _, err := database.NewUpdate().Model((*chainStateModel)(nil)).
		Set("watermark = ?", count).Where("id = ?", 1).Exec(context.Background()); err != nil {
		t.Fatalf("record chain start: %v", err)
	}
}

// A fresh install runs the watermark migration against an empty audit_events, so
// the recorded chain start is 0 and there is nothing to walk. That must read as
// intact rather than as a missing chain start, or every new node would report its
// own audit log as tampered with before it had written a single event.
func TestVerifyAcceptsAFreshDatabase(t *testing.T) {
	log, _ := newChainModule(t)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if !result.Intact || result.Checked != 0 || result.Unhashed != 0 || result.FirstBrokenID != nil {
		t.Fatalf("fresh database did not verify: %+v", result)
	}
}

// A database upgraded from before the chain shipped holds rows with no hash.
// Those must read as intact, not as tampering, while everything written after
// the upgrade still chains and verifies.
func TestVerifyToleratesPreChainRows(t *testing.T) {
	log, database := newChainModule(t)
	insertPreChainRows(t, database, 2)
	recordChain(t, log, 3)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if !result.Intact || result.Unhashed != 2 || result.Checked != 3 {
		t.Fatalf("upgraded database did not verify: %+v", result)
	}
}

// Blanking the hash columns is the cheapest tampering there is: without a
// recorded chain start every row would pass as a legacy row and the log would
// read clean. It must report as broken at the first blanked row.
func TestVerifyDetectsBlankedHashes(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 3)
	blankHashes(t, database)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.FirstBrokenID == nil || *result.FirstBrokenID != 1 {
		t.Fatalf("blanked chain was not reported: %+v", result)
	}
	// And the chain must not silently restart from genesis afterwards.
	if err := log.Record(context.Background(), Entry{Action: "identity.login", Subject: "user:admin"}); err == nil {
		t.Fatal("Record continued a blanked chain")
	}
}

// Deleting the recorded chain start would put every row back below the
// watermark, so a missing boundary is tampering rather than "no chain yet".
func TestVerifyDetectsAMissingChainStart(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 2)
	if _, err := database.NewDelete().Model((*chainStateModel)(nil)).Where("id = ?", 1).Exec(context.Background()); err != nil {
		t.Fatalf("delete chain start: %v", err)
	}

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.Reason == "" {
		t.Fatalf("missing chain start was not reported: %+v", result)
	}
}

// Dropping the whole table is cheaper than deleting its row, and used to surface
// as an unavailable-database error the operator would read as a panel bug.
func TestVerifyDetectsADroppedChainStartTable(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 2)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_chain_state"); err != nil {
		t.Fatalf("drop chain start table: %v", err)
	}

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.Reason == "" {
		t.Fatalf("dropped chain start table was not reported: %+v", result)
	}
}

// Blanking the hashes and then pushing the watermark past every row is the
// two-statement rewrite that would otherwise pass every blanked row off as a
// pre-chain one.
func TestVerifyDetectsAWatermarkPastTheNewestRow(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 5)
	blankHashes(t, database)
	setWatermark(t, database, 1000000)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.Reason == "" {
		t.Fatalf("moved chain boundary was not reported: %+v", result)
	}
}

// Landing the watermark exactly on the newest row keeps it in range, so the
// giveaway is that a started chain has hashed nothing at all.
func TestVerifyDetectsABlankedChainRebasedOntoTheNewestRow(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 5)
	blankHashes(t, database)
	setWatermark(t, database, 5)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.Unhashed != 5 {
		t.Fatalf("rebased blank chain was not reported: %+v", result)
	}
}

// Every verdict carries the guarantee text, because "intact" alone reads as a
// stronger claim than an in-database chain can make.
func TestVerifyAlwaysReportsTheGuarantee(t *testing.T) {
	log, _ := newChainModule(t)
	recordChain(t, log, 1)

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Guarantee == "" {
		t.Fatalf("verdict carried no guarantee: %+v", result)
	}
}

func blankHashes(t *testing.T, database *bun.DB) {
	t.Helper()
	if _, err := database.NewUpdate().Model((*eventModel)(nil)).
		Set("prev_hash = ?", "").Set("hash = ?", "").Where("id > ?", 0).Exec(context.Background()); err != nil {
		t.Fatalf("strip hashes: %v", err)
	}
}

func setWatermark(t *testing.T, database *bun.DB, watermark int64) {
	t.Helper()
	if _, err := database.NewUpdate().Model((*chainStateModel)(nil)).
		Set("watermark = ?", watermark).Where("id = ?", 1).Exec(context.Background()); err != nil {
		t.Fatalf("move chain start: %v", err)
	}
}

// Two processes share control.db — the panel and, say, a backup run from its
// systemd timer. A commit from the other process must not fail the panel's audit
// write, which is fail-closed for sensitive actions.
func TestRecordSurvivesAConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	first, err := persistence.Open(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if err := persistence.RunMigrations(context.Background(), first); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	second, err := persistence.Open(path)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	logs := make([]*Module, 0, 2)
	for _, database := range []*bun.DB{first, second} {
		log, err := New(context.Background(), database)
		if err != nil {
			t.Fatalf("New returned an error: %v", err)
		}
		logs = append(logs, log)
	}

	const perWriter = 20
	errs := make(chan error, len(logs)*perWriter)
	var writers sync.WaitGroup
	for _, log := range logs {
		writers.Add(1)
		go func(log *Module) {
			defer writers.Done()
			for index := 0; index < perWriter; index++ {
				errs <- log.Record(context.Background(), Entry{
					Action: "identity.login", Subject: "user:admin", Metadata: map[string]any{"sequence": index},
				})
			}
		}(log)
	}
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Record failed under a concurrent writer: %v", err)
		}
	}

	result, err := logs[0].Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if !result.Intact || result.Checked != int64(len(logs)*perWriter) {
		t.Fatalf("concurrent writes did not chain: %+v", result)
	}
}

// An unhashed row appearing after the chain has started can only have been
// inserted by hand, and must not pass as a legacy row.
func TestVerifyDetectsAnInjectedUnhashedRow(t *testing.T) {
	log, database := newChainModule(t)
	recordChain(t, log, 2)
	injected := &eventModel{
		OccurredAt: log.now().UTC(), Action: "firewall.rule_allowed", Subject: "firewall-rule:2222/tcp", Metadata: "{}",
	}
	if _, err := database.NewInsert().Model(injected).Exec(context.Background()); err != nil {
		t.Fatalf("insert row: %v", err)
	}

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if result.Intact || result.FirstBrokenID == nil || *result.FirstBrokenID != injected.ID {
		t.Fatalf("injected row was not reported: %+v", result)
	}
}

func TestRecordSensitiveFailsClosed(t *testing.T) {
	log, database := newChainModule(t)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := NewSink(log, nil).RecordSensitive(context.Background(), Entry{Action: "firewall.enabled", Subject: "firewall"}); err == nil {
		t.Fatal("RecordSensitive accepted an unwritable audit event")
	}
}

// TestVerifySurvivesANanosecondClock pins the one bug the rest of this file
// cannot catch on a developer machine: macOS reads the clock at microsecond
// granularity, so a hash taken over the raw reading happens to match the value
// SQLite stores, while on Linux the extra nanoseconds are truncated away on
// write and every row verifies as tampered with. Forcing a sub-microsecond
// clock reproduces a Linux node regardless of the host this runs on.
func TestVerifySurvivesANanosecondClock(t *testing.T) {
	log, _ := newChainModule(t)
	instant := time.Date(2026, time.July, 22, 11, 36, 39, 625375123, time.UTC)
	log.now = func() time.Time {
		instant = instant.Add(1234567 * time.Nanosecond)
		return instant
	}
	for index := 0; index < 3; index++ {
		if err := log.Record(context.Background(), Entry{
			Action: "firewall.rule_allowed", Subject: "firewall-rule:2222/tcp",
		}); err != nil {
			t.Fatalf("Record returned an error: %v", err)
		}
	}

	result, err := log.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
	if !result.Intact || result.Checked != 3 {
		t.Fatalf("a chain written by a nanosecond clock did not verify: %+v", result)
	}
}
