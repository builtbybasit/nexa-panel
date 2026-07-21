package audit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

func TestRecordAndList(t *testing.T) {
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
	if err := log.Record(context.Background(), Entry{
		Action: "identity.login", Subject: "user:admin", Metadata: map[string]any{"result": "success"},
	}); err != nil {
		t.Fatalf("Record returned an error: %v", err)
	}
	events, err := log.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("List returned an error: %v", err)
	}
	if len(events) != 1 || events[0].Action != "identity.login" {
		t.Fatalf("unexpected events: %+v", events)
	}
}
