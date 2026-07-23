package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
)

const gib = uint64(1024 * 1024 * 1024)

// diskProbeTimeout bounds one filesystem measurement. statfs on a dead NFS or
// other network mount blocks in the kernel for as long as the server stays
// gone, which would leave the installer waiting with nothing on screen.
const diskProbeTimeout = 5 * time.Second

// Filesystem is one directory the panel writes to and the space it needs there.
// State, configuration and the site root are frequently separate mounts, so
// each is measured on its own rather than assuming one root filesystem.
type Filesystem struct {
	Label       string
	Path        string
	Minimum     uint64
	Recommended uint64
}

// DiskCheck measures free space where the panel will write. Minimum is what an
// install needs to complete; Recommended is what it needs to keep running once
// databases, backups and site content accumulate.
type DiskCheck struct {
	Filesystems []Filesystem
	Free        func(path string) (uint64, error)
}

func NewDiskCheck(statePath, configDir, siteRoot string) DiskCheck {
	return DiskCheck{
		Filesystems: []Filesystem{
			{Label: "state", Path: filepath.Dir(statePath), Minimum: 2 * gib, Recommended: 10 * gib},
			{Label: "config", Path: configDir, Minimum: 128 * 1024 * 1024, Recommended: 512 * 1024 * 1024},
			{Label: "sites", Path: siteRoot, Minimum: 2 * gib, Recommended: 20 * gib},
		},
		Free: freeBytes,
	}
}

func (c DiskCheck) Name() string { return "disk" }

func (c DiskCheck) Run(ctx context.Context) Result {
	const title = "Disk"
	var short, tight, fine, unknown []string
	for _, filesystem := range c.Filesystems {
		free, err := c.measure(ctx, filesystem.Path)
		if err != nil {
			unknown = append(unknown, fmt.Sprintf("%s %s (%v)", filesystem.Label, filesystem.Path, err))
			continue
		}
		summary := fmt.Sprintf("%s %s free on %s", filesystem.Label, capacity.FormatBytes(free), filesystem.Path)
		switch {
		case free < filesystem.Minimum:
			short = append(short, fmt.Sprintf("%s, needs %s", summary, capacity.FormatBytes(filesystem.Minimum)))
		case free < filesystem.Recommended:
			tight = append(tight, fmt.Sprintf("%s, %s recommended", summary, capacity.FormatBytes(filesystem.Recommended)))
		default:
			fine = append(fine, summary)
		}
	}

	if len(short) > 0 {
		return blocker(c.Name(), title, strings.Join(short, "; "), "Free up space or grow the volume before installing.")
	}
	if len(unknown) > 0 {
		return warning(c.Name(), title, "cannot measure "+strings.Join(unknown, "; "), "Confirm by hand that these paths have room.")
	}
	if len(tight) > 0 {
		return warning(c.Name(), title, strings.Join(tight, "; "), "The install fits, but site content, databases and backups will fill this quickly.")
	}
	return ok(c.Name(), title, strings.Join(fine, "; "))
}

// measure runs Free off the calling goroutine, because an uninterruptible
// syscall cannot be cancelled: the goroutine is abandoned when the deadline
// passes and ends whenever the kernel finally answers. A path that cannot be
// measured in time degrades to a warning like any other measurement failure.
func (c DiskCheck) measure(ctx context.Context, path string) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, diskProbeTimeout)
	defer cancel()

	type measurement struct {
		free uint64
		err  error
	}
	measured := make(chan measurement, 1)
	go func() {
		free, err := c.Free(path)
		measured <- measurement{free: free, err: err}
	}()

	select {
	case result := <-measured:
		return result.free, result.err
	case <-ctx.Done():
		return 0, fmt.Errorf("measure %s: %w", path, ctx.Err())
	}
}

// freeBytes measures the filesystem that will hold path. The panel's
// directories do not exist yet on a first install, so it walks up to the
// nearest ancestor that does — that is the mount the new directory lands on.
func freeBytes(path string) (uint64, error) {
	existing := path
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return 0, fmt.Errorf("no existing ancestor of %s", path)
		}
		existing = parent
	}
	return availableBytes(existing)
}
