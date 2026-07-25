package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemDiscovererReturnsInstalledFPMRuntimes(t *testing.T) {
	root := t.TempDir()
	// 7.3 is a sub-7.4 legacy version that must stay out; 9.0 is a future major
	// that must be admitted so sites can target it once it publishes.
	for _, version := range []string{"7.3", "7.4", "8.0", "8.3", "8.4", "9.0", "not-a-version"} {
		if err := os.MkdirAll(filepath.Join(root, version, "fpm", "pool.d"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "8.5", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}

	module, err := New(FilesystemDiscoverer{PHPConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	items, err := module.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 || items[0].Version != "7.4" || items[1].Version != "8.0" ||
		items[2].Version != "8.3" || items[3].Version != "8.4" || items[4].Version != "9.0" {
		t.Fatalf("unexpected runtimes: %+v", items)
	}
	if items[0].SupportStatus != "end_of_life_allowed" || items[1].SupportStatus != "allowed_by_policy" {
		t.Fatalf("unexpected support policy: %+v", items)
	}
	for _, version := range []string{"7.4", "9.0"} {
		allowed, err := module.Allowed(context.Background(), version)
		if err != nil || !allowed {
			t.Fatalf("PHP %s allowed = %v, %v", version, allowed, err)
		}
	}
	if allowed, err := module.Allowed(context.Background(), "7.3"); err != nil || allowed {
		t.Fatalf("PHP 7.3 allowed = %v, %v (want false)", allowed, err)
	}
}
