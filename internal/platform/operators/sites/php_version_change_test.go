package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// versionAwareSystem extends the plain fake with per-version PHP reload
// recording, which is the whole point of a runtime change: two FPM masters
// must be reloaded, in an order that hands the socket over cleanly.
type versionAwareSystem struct {
	fakeNodeSystem
	phpReloads    []string
	failReloadFor string
}

func (s *versionAwareSystem) ReloadPHP(ctx context.Context, version string) error {
	s.phpReloads = append(s.phpReloads, version)
	if s.failReloadFor == version {
		return errors.New("injected reload failure")
	}
	_ = s.fakeNodeSystem.call("reload-php")
	return nil
}

func oldPoolPath(operator *HostOperator, site Site) string {
	return filepath.Join(operator.renderer.PHPConfigRoot, site.RetiredPHPVersion, "fpm", "pool.d", "nexa-"+site.Slug+".conf")
}

func versionChangeFixture(t *testing.T, system NodeSystem) (*HostOperator, Site) {
	t.Helper()
	operator, site := testHostOperator(t, system)
	site.RetiredPHPVersion = "8.3"
	// The outgoing version's pool exists on the node, as it would after the
	// site had been active on 8.3.
	old := oldPoolPath(operator, site)
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("stale 8.3 pool"), 0o640); err != nil {
		t.Fatal(err)
	}
	return operator, site
}

func TestRenderRetiresTheOutgoingPoolOnAVersionChange(t *testing.T) {
	operator, site := testHostOperator(t, new(fakeNodeSystem))
	baseline, err := operator.renderer.Render(site)
	if err != nil {
		t.Fatal(err)
	}
	site.RetiredPHPVersion = "8.3"
	changed, err := operator.renderer.Render(site)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Retired) != len(baseline.Retired)+1 {
		t.Fatalf("retired = %v, want the baseline plus the outgoing pool", changed.Retired)
	}
	last := changed.Retired[len(changed.Retired)-1]
	if !strings.Contains(last, filepath.Join("8.3", "fpm", "pool.d")) {
		t.Fatalf("retired path = %s, want the 8.3 pool", last)
	}
	// A no-op marker must not retire the live pool.
	site.RetiredPHPVersion = site.PHPVersion
	same, err := operator.renderer.Render(site)
	if err != nil {
		t.Fatal(err)
	}
	if len(same.Retired) != len(baseline.Retired) {
		t.Fatalf("retired = %v, want the baseline when old == new", same.Retired)
	}
}

func TestApplyRemovesTheOldPoolAndReloadsBothVersionsInOrder(t *testing.T) {
	system := new(versionAwareSystem)
	operator, site := versionChangeFixture(t, system)
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPoolPath(operator, site)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the outgoing version's pool must be removed by the apply")
	}
	// Old first (release the socket), new second (bind it).
	if !reflect.DeepEqual(system.phpReloads, []string{"8.3", "8.4"}) {
		t.Fatalf("php reloads = %v, want [8.3 8.4]", system.phpReloads)
	}
}

// The old runtime may already be uninstalled — often the very reason for the
// change — so a failing reload of the outgoing version must not fail the site.
func TestApplyToleratesAMissingOutgoingRuntime(t *testing.T) {
	system := &versionAwareSystem{failReloadFor: "8.3"}
	operator, site := versionChangeFixture(t, system)
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatalf("Apply() = %v, want success despite the outgoing reload failing", err)
	}
}

func TestRollbackRestoresTheOldPoolAndReloadsNewThenOld(t *testing.T) {
	system := new(versionAwareSystem)
	operator, site := versionChangeFixture(t, system)
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	system.phpReloads = nil
	if _, err := operator.Rollback(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(oldPoolPath(operator, site))
	if err != nil || string(content) != "stale 8.3 pool" {
		t.Fatalf("old pool after rollback = %q, %v; want the pre-change content restored", content, err)
	}
	// New first (drop its pool, release the socket), old second (re-bind).
	if !reflect.DeepEqual(system.phpReloads, []string{"8.4", "8.3"}) {
		t.Fatalf("php reloads = %v, want [8.4 8.3]", system.phpReloads)
	}
}
