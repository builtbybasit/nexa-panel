package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemDiscovererReturnsInstalledPHP8FPMOnly(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"7.4", "8.0", "8.3", "8.4", "not-a-version"} {
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
	if len(items) != 4 || items[0].Version != "7.4" || items[1].Version != "8.0" || items[2].Version != "8.3" || items[3].Version != "8.4" {
		t.Fatalf("unexpected runtimes: %+v", items)
	}
	if items[0].SupportStatus != "end_of_life_allowed" || items[1].SupportStatus != "allowed_by_policy" {
		t.Fatalf("unexpected support policy: %+v", items)
	}
	allowed, err := module.Allowed(context.Background(), "7.4")
	if err != nil || !allowed {
		t.Fatalf("PHP 7.4 allowed = %v, %v", allowed, err)
	}
}
