package databases

import (
	"context"
	"errors"
	"testing"
	"time"
)

func sizeHarness(t *testing.T, engineKey string, databases ...string) *testHarness {
	t.Helper()
	h := newTestModule(t)
	h.seedUser(t, engineKey, "user_owner", "app_owner")
	for _, name := range databases {
		h.seedDatabase(t, engineKey, "database_"+name, name, "user_owner")
	}
	return h
}

func (h *testHarness) setSizes(key string, sizes map[string]int64) {
	if key == "mysql" {
		h.mysql.sizes = sizes
		return
	}
	h.postgres.sizes = sizes
}

func (h *testHarness) sizeCalls(key string) int {
	if key == "mysql" {
		return h.mysql.sizeCalls
	}
	return h.postgres.sizeCalls
}

func TestDatabaseSizesArePersistedAndReusedWithinTheRefreshInterval(t *testing.T) {
	for _, engineKey := range []string{"mysql", "postgresql"} {
		t.Run(engineKey, func(t *testing.T) {
			ctx := context.Background()
			h := sizeHarness(t, engineKey, "app_db", "webmail")
			h.setSizes(engineKey, map[string]int64{"app_db": 491520, "webmail": 278528})
			clock := testClock
			h.module.now = func() time.Time { return clock }

			items, err := h.module.SyncDatabaseSizes(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 2 || items[0].SizeBytes == nil || *items[0].SizeBytes != 491520 || items[1].SizeBytes == nil || *items[1].SizeBytes != 278528 {
				t.Fatalf("sizes were not persisted: %+v", items)
			}
			// One query covers every database on the server; probing per
			// database would multiply agent round trips by rows on screen.
			if h.sizeCalls(engineKey) != 1 {
				t.Fatalf("probes = %d, want 1 for two databases on one server", h.sizeCalls(engineKey))
			}
			if items[0].SizeObservedAt == nil || !items[0].SizeObservedAt.Equal(testClock) {
				t.Fatalf("observedAt = %v, want the measurement time", items[0].SizeObservedAt)
			}

			// Inside the interval the stored value is served as-is, even though
			// the host has since changed.
			clock = testClock.Add(sizeRefreshInterval - time.Second)
			h.setSizes(engineKey, map[string]int64{"app_db": 999999, "webmail": 999999})
			items, err = h.module.SyncDatabaseSizes(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if h.sizeCalls(engineKey) != 1 {
				t.Fatalf("probes = %d, want no re-probe inside the refresh interval", h.sizeCalls(engineKey))
			}
			if *items[0].SizeBytes != 491520 {
				t.Fatalf("cached size = %d, want the previously measured 491520", *items[0].SizeBytes)
			}

			// Once it lapses, the next read re-measures.
			clock = testClock.Add(sizeRefreshInterval + time.Second)
			items, err = h.module.SyncDatabaseSizes(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if h.sizeCalls(engineKey) != 2 {
				t.Fatalf("probes = %d, want a re-probe after the refresh interval lapsed", h.sizeCalls(engineKey))
			}
			if *items[0].SizeBytes != 999999 {
				t.Fatalf("refreshed size = %d, want 999999", *items[0].SizeBytes)
			}
		})
	}
}

// A server can be down, starting, or mid-restore while its databases still
// need to render, so a failed probe must not take the list down with it.
func TestDatabaseSizeProbeFailureKeepsTheLastKnownSizeAndStillServes(t *testing.T) {
	ctx := context.Background()
	h := sizeHarness(t, "postgresql", "app_db")
	h.setSizes("postgresql", map[string]int64{"app_db": 491520})
	clock := testClock
	h.module.now = func() time.Time { return clock }

	if _, err := h.module.SyncDatabaseSizes(ctx, ""); err != nil {
		t.Fatal(err)
	}

	h.postgres.sizeError = errors.New("the database system is starting up")
	clock = testClock.Add(sizeRefreshInterval + time.Second)
	items, err := h.module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatalf("a failed size probe took down the whole list: %v", err)
	}
	if len(items) != 1 || items[0].SizeBytes == nil || *items[0].SizeBytes != 491520 {
		t.Fatalf("last known size was lost on probe failure: %+v", items)
	}
	// observedAt stays at the last real measurement so callers can see the
	// number has gone stale rather than trusting it as current.
	if items[0].SizeObservedAt == nil || !items[0].SizeObservedAt.Equal(testClock) {
		t.Fatalf("observedAt = %v, want the last successful measurement", items[0].SizeObservedAt)
	}
}

func TestDatabaseMeasuredAtZeroIsDistinctFromNeverMeasured(t *testing.T) {
	ctx := context.Background()
	h := sizeHarness(t, "mysql", "empty_db", "vanished_db")
	// empty_db exists on the host and holds nothing; vanished_db was dropped
	// out of band and so is absent from the probe entirely.
	h.setSizes("mysql", map[string]int64{"empty_db": 0})
	h.module.now = func() time.Time { return testClock }

	items, err := h.module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].SizeBytes == nil || *items[0].SizeBytes != 0 {
		t.Fatalf("empty_db size = %v, want a present zero rather than nil", items[0].SizeBytes)
	}
	if items[1].SizeBytes != nil {
		t.Fatalf("vanished_db size = %v, want nil rather than a fabricated zero", *items[1].SizeBytes)
	}
}

func TestDatabaseSizesSkipServersWithNothingActiveToMeasure(t *testing.T) {
	ctx := context.Background()
	h := newTestModule(t)
	h.module.now = func() time.Time { return testClock }

	if _, err := h.module.SyncDatabaseSizes(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if h.sizeCalls("mysql")+h.sizeCalls("postgresql") != 0 {
		t.Fatalf("probes = %d, want none when there is no active database to measure", h.sizeCalls("mysql")+h.sizeCalls("postgresql"))
	}
}
