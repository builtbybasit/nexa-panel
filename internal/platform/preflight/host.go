package preflight

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
)

// PlatformCheck restates in Go what scripts/install.sh asserts in shell, so a
// `nexa doctor` run on a candidate host answers "can I install here?" without
// downloading the installer first.
type PlatformCheck struct {
	OSRelease func() (map[string]string, error)
	Arch      string
	OS        string
}

func NewPlatformCheck() PlatformCheck {
	return PlatformCheck{OSRelease: readOSRelease, Arch: runtime.GOARCH, OS: runtime.GOOS}
}

func (c PlatformCheck) Name() string { return "platform" }

func (c PlatformCheck) Run(context.Context) Result {
	const title = "Platform"
	if c.OS != "linux" {
		return blocker(c.Name(), title, fmt.Sprintf("%s is not a supported operating system", c.OS), "Nexa Panel manages a Linux node; install it on Ubuntu 24.04 LTS.")
	}
	if c.Arch != "amd64" && c.Arch != "arm64" {
		return blocker(c.Name(), title, fmt.Sprintf("unsupported architecture %s", c.Arch), "Nexa Panel publishes amd64 and arm64 builds only.")
	}

	release, err := c.OSRelease()
	if err != nil {
		return blocker(c.Name(), title, fmt.Sprintf("cannot identify this host (%v)", err), "Nexa Panel targets Ubuntu 24.04 LTS; /etc/os-release must be readable.")
	}
	name := release["PRETTY_NAME"]
	if name == "" {
		name = release["ID"] + " " + release["VERSION_ID"]
	}
	if release["ID"] != "ubuntu" {
		return blocker(c.Name(), title, fmt.Sprintf("%s is not supported", name), "Nexa Panel targets Ubuntu 24.04 LTS (the PHP repository it manages publishes for Ubuntu only).")
	}
	if release["VERSION_ID"] != "24.04" {
		return blocker(c.Name(), title, fmt.Sprintf("%s is not supported", name), "Nexa Panel currently supports Ubuntu 24.04 LTS only.")
	}
	if release["VERSION_CODENAME"] == "" {
		return blocker(c.Name(), title, "this Ubuntu release reports no VERSION_CODENAME", "The package repositories the installer configures are keyed on the release codename.")
	}
	return ok(c.Name(), title, fmt.Sprintf("%s (%s), %s", name, release["VERSION_CODENAME"], c.Arch))
}

func readOSRelease() (map[string]string, error) {
	const path = "/etc/os-release"
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()

	release, err := parseOSRelease(file)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return release, nil
}

func parseOSRelease(source io.Reader) (map[string]string, error) {
	release := make(map[string]string)
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		release[key] = strings.Trim(value, `"'`)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan os-release: %w", err)
	}
	return release, nil
}

// SystemdCheck reports whether systemd is the init system. The panel is a set
// of units; without systemd nothing the installer enables can ever start.
type SystemdCheck struct {
	Exists func(path string) bool
}

func NewSystemdCheck() SystemdCheck { return SystemdCheck{Exists: pathExists} }

func (c SystemdCheck) Name() string { return "systemd" }

func (c SystemdCheck) Run(context.Context) Result {
	const title = "systemd"
	if c.Exists("/run/systemd/system") {
		return ok(c.Name(), title, "running")
	}
	if c.Exists("/usr/lib/systemd/systemd") || c.Exists("/lib/systemd/systemd") {
		// An image build has systemd installed but not as pid 1; the installer
		// enables the units and they start on first boot.
		return warning(c.Name(), title, "installed but not running as init here", "The panel's units will be enabled now and start on the host's first boot.")
	}
	return blocker(c.Name(), title, "not installed", "Nexa Panel runs as systemd units; install systemd (systemd-sysv) first.")
}

// installerMinimumMemory is the floor the installer refuses below: 1.8 GiB.
// The advertised minimum is 2 GiB, but a host sold as 2 GB never reports 2 GiB
// of MemTotal — firmware and the kernel reserve their share first, so a stock
// DigitalOcean/Hetzner/Vultr 2 GB VM reports around 1.94 GiB. capacity.Classify
// tests MemTotal against a flat 2 GiB because it is sizing workloads, not
// admitting hosts; gating on it would refuse the documented minimum spec. The
// 200 MiB of headroom here clears the largest reservations seen in the wild and
// still sits far above the 1 GiB tier, which cannot reach it.
const installerMinimumMemory = 9 * gib / 5

// MemoryCheck reuses the capacity profile the panel itself sizes workloads by,
// so doctor and the API agree on what this host is rated for.
type MemoryCheck struct {
	Reader capacity.Reader
}

func NewMemoryCheck() MemoryCheck { return MemoryCheck{Reader: capacity.NewProcReader()} }

func (c MemoryCheck) Name() string { return "memory" }

func (c MemoryCheck) Run(ctx context.Context) Result {
	const title = "Memory"
	snapshot, err := c.Reader.Read(ctx)
	if err != nil {
		return warning(c.Name(), title, fmt.Sprintf("unavailable (%v)", err), "Confirm this host has at least 2 GiB of RAM.")
	}

	detail := fmt.Sprintf(
		"%s total, %s available, profile %s",
		capacity.FormatBytes(snapshot.TotalBytes),
		capacity.FormatBytes(snapshot.AvailableBytes),
		snapshot.Profile,
	)
	if snapshot.TotalBytes < installerMinimumMemory {
		return blocker(c.Name(), title, detail, fmt.Sprintf(
			"Nexa Panel needs at least 2 GiB of RAM and this host reports %s; resize it before installing.",
			capacity.FormatBytes(snapshot.TotalBytes),
		))
	}
	if snapshot.Profile == capacity.ProfileUnsupported {
		return warning(c.Name(), title, detail, fmt.Sprintf(
			"%s is just under the 2 GiB the panel is rated for — normal for a 2 GB host, whose firmware and kernel reserve the rest. Expect to run close to the limit.",
			capacity.FormatBytes(snapshot.TotalBytes),
		))
	}
	return ok(c.Name(), title, detail)
}

// PodmanCheck is advisory: only the pgAdmin database web client deploys as a
// Quadlet unit, so an old or missing Podman costs one feature rather than the
// install.
type PodmanCheck struct {
	Inspect func(ctx context.Context) (podman.Status, error)
}

func NewPodmanCheck() PodmanCheck {
	inspector := podman.NewInspector()
	return PodmanCheck{Inspect: inspector.Inspect}
}

func (c PodmanCheck) Name() string { return "podman" }

func (c PodmanCheck) Run(ctx context.Context) Result {
	const title = "Podman"
	status, err := c.Inspect(ctx)
	if err != nil {
		return warning(c.Name(), title, fmt.Sprintf("unavailable (%v)", err), "The installer installs Podman; only the pgAdmin web client needs it.")
	}
	return ok(c.Name(), title, fmt.Sprintf("%s at %s", status.Version, status.Path))
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
