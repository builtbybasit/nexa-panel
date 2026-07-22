package preflight

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
)

func existsIn(paths ...string) func(string) bool {
	present := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		present[path] = struct{}{}
	}
	return func(path string) bool {
		_, found := present[path]
		return found
	}
}

func unitsIn(states map[string]UnitState) func(context.Context, string) UnitState {
	return func(_ context.Context, unit string) UnitState { return states[unit] }
}

func TestPortsCheck(t *testing.T) {
	tests := []struct {
		name      string
		listeners []Listener
		err       error
		want      Status
		contains  string
	}{
		{name: "all free", want: StatusOK, contains: "80, 443, 8888 are free"},
		{
			name:      "foreign server holds a port",
			listeners: []Listener{{Port: 80, Process: "apache2", PID: 812}},
			want:      StatusBlocker,
			contains:  "80 (apache2, pid 812)",
		},
		{
			name:      "unrelated port ignored",
			listeners: []Listener{{Port: 22, Process: "sshd"}},
			want:      StatusOK,
		},
		{
			name:      "our own nginx is an upgrade, not a conflict",
			listeners: []Listener{{Port: 80, Process: "nginx", PID: 4}},
			want:      StatusWarning,
			contains:  "80 (nginx, pid 4)",
		},
		{
			name:     "enumeration failure degrades to a warning",
			err:      errors.New("no ss"),
			want:     StatusWarning,
			contains: "cannot enumerate",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := NewPortsCheck([]int{80, 443, 8888})
			check.Listeners = func(context.Context) ([]Listener, error) { return test.listeners, test.err }

			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

func TestParseListeners(t *testing.T) {
	output := strings.Join([]string{
		`LISTEN 0      511          0.0.0.0:80        0.0.0.0:*    users:(("nginx",pid=812,fd=6),("nginx",pid=811,fd=6))`,
		`LISTEN 0      4096       127.0.0.1:8888      0.0.0.0:*`,
		`LISTEN 0      511             [::]:443          [::]:*    users:(("apache2",pid=99,fd=4))`,
		``,
	}, "\n")

	listeners := parseListeners(output)
	want := []Listener{
		{Port: 80, Process: "nginx", PID: 812},
		{Port: 8888},
		{Port: 443, Process: "apache2", PID: 99},
	}
	if len(listeners) != len(want) {
		t.Fatalf("parsed %d listeners, want %d: %+v", len(listeners), len(want), listeners)
	}
	for i, listener := range listeners {
		if listener != want[i] {
			t.Fatalf("listener %d = %+v, want %+v", i, listener, want[i])
		}
	}
}

func TestExistingInstallCheck(t *testing.T) {
	const state = "/var/lib/nexa-panel/control.db"
	keys := []string{"/etc/nexa-panel/master.key", "/var/lib/nexa-panel/master.key"}

	tests := []struct {
		name          string
		exists        func(string) bool
		units         map[string]UnitState
		allowExisting bool
		want          Status
		contains      string
	}{
		{name: "clean host", exists: existsIn(), want: StatusOK, contains: "none found"},
		{
			name:     "running panel blocks",
			exists:   existsIn(state),
			units:    map[string]UnitState{"nexa-api.service": {Present: true, Active: true}},
			want:     StatusBlocker,
			contains: "nexa-api.service",
		},
		{
			name:          "operator opts in to the in-place upgrade",
			exists:        existsIn(state),
			units:         map[string]UnitState{"nexa-api.service": {Present: true, Active: true}},
			allowExisting: true,
			want:          StatusWarning,
			contains:      "upgrading in place",
		},
		{
			name:     "stopped install is an upgrade",
			exists:   existsIn(state, keys[0]),
			units:    map[string]UnitState{"nexa-api.service": {Present: true}},
			want:     StatusWarning,
			contains: "master key /etc/nexa-panel/master.key",
		},
		{
			name:     "a legacy master key alone still counts",
			exists:   existsIn(keys[1]),
			want:     StatusWarning,
			contains: "/var/lib/nexa-panel/master.key",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := NewExistingInstallCheck(state, keys, test.allowExisting)
			check.Exists = test.exists
			check.Unit = unitsIn(test.units)

			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

func TestCompetingPanelCheck(t *testing.T) {
	tests := []struct {
		name     string
		exists   func(string) bool
		units    map[string]UnitState
		want     Status
		contains string
	}{
		{name: "clean host", exists: existsIn(), want: StatusOK, contains: "none found"},
		{name: "cPanel installed", exists: existsIn("/usr/local/cpanel"), want: StatusBlocker, contains: "cPanel"},
		{name: "Plesk at the alternate prefix", exists: existsIn("/opt/psa"), want: StatusBlocker, contains: "Plesk"},
		{name: "CyberPanel installed", exists: existsIn("/usr/local/CyberCP"), want: StatusBlocker, contains: "CyberPanel"},
		{name: "Webmin installed", exists: existsIn("/etc/webmin"), want: StatusBlocker, contains: "Webmin"},
		{
			name:     "Apache running fights the panel's Nginx",
			exists:   existsIn("/etc/apache2"),
			units:    map[string]UnitState{"apache2.service": {Present: true, Active: true}},
			want:     StatusBlocker,
			contains: "apache2.service is running",
		},
		{
			name:     "Apache installed but stopped only warns",
			exists:   existsIn("/etc/apache2"),
			units:    map[string]UnitState{"apache2.service": {Present: true}},
			want:     StatusWarning,
			contains: "Apache (installed, not running)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := NewCompetingPanelCheck()
			check.Exists = test.exists
			check.Unit = unitsIn(test.units)

			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

func TestDiskCheck(t *testing.T) {
	tests := []struct {
		name     string
		free     map[string]uint64
		err      error
		want     Status
		contains string
	}{
		{
			name: "roomy",
			free: map[string]uint64{"/var/lib/nexa-panel": 40 * gib, "/etc/nexa-panel": 40 * gib, "/srv/nexa": 40 * gib},
			want: StatusOK,
		},
		{
			name:     "state filesystem too small to install",
			free:     map[string]uint64{"/var/lib/nexa-panel": gib, "/etc/nexa-panel": 40 * gib, "/srv/nexa": 40 * gib},
			want:     StatusBlocker,
			contains: "state 1.0 GiB free on /var/lib/nexa-panel",
		},
		{
			name:     "enough to install, not enough to live in",
			free:     map[string]uint64{"/var/lib/nexa-panel": 3 * gib, "/etc/nexa-panel": 40 * gib, "/srv/nexa": 40 * gib},
			want:     StatusWarning,
			contains: "recommended",
		},
		{
			name:     "unmeasurable",
			err:      errors.New("statfs failed"),
			want:     StatusWarning,
			contains: "cannot measure",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := NewDiskCheck("/var/lib/nexa-panel/control.db", "/etc/nexa-panel", "/srv/nexa")
			check.Free = func(path string) (uint64, error) {
				if test.err != nil {
					return 0, test.err
				}
				return test.free[path], nil
			}

			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

// TestDiskCheckHungMount stands in for a dead NFS mount: statfs never returns,
// and the check has to report rather than hold the installer open.
func TestDiskCheckHungMount(t *testing.T) {
	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })

	check := NewDiskCheck("/var/lib/nexa-panel/control.db", "/etc/nexa-panel", "/srv/nexa")
	check.Free = func(string) (uint64, error) {
		<-hung
		return 0, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", result.Status, StatusWarning, result.Detail)
	}
	if !strings.Contains(result.Detail, "cannot measure") {
		t.Fatalf("detail %q does not report an unmeasurable path", result.Detail)
	}
}

func TestNetworkCheck(t *testing.T) {
	endpoints := []Endpoint{
		{Label: "Ubuntu archive", Host: "archive.ubuntu.com", URL: "http://archive.ubuntu.com/", Required: true},
		{Label: "Node.js release index", Host: "nodejs.org", URL: "https://nodejs.org/dist/index.json"},
	}

	tests := []struct {
		name       string
		unresolved string
		unreached  string
		untrusted  string
		want       Status
		contains   string
	}{
		{name: "everything reachable", want: StatusOK, contains: "2 package sources reachable"},
		{
			name:       "no DNS at all",
			unresolved: "archive.ubuntu.com",
			want:       StatusBlocker,
			contains:   "Ubuntu archive",
		},
		{
			name:      "resolves but cannot be fetched",
			unreached: "http://archive.ubuntu.com/",
			want:      StatusBlocker,
			contains:  "Ubuntu archive",
		},
		{
			name:       "optional source only warns",
			unresolved: "nodejs.org",
			want:       StatusWarning,
			contains:   "Node.js release index",
		},
		{
			name:      "an unverifiable certificate is not an offline host",
			untrusted: "http://archive.ubuntu.com/",
			want:      StatusWarning,
			contains:  "Ubuntu archive",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := NewNetworkCheck(endpoints)
			check.Resolve = func(_ context.Context, host string) error {
				if host == test.unresolved {
					return errors.New("no such host")
				}
				return nil
			}
			check.Reach = func(_ context.Context, url string) error {
				switch url {
				case test.unreached:
					return errors.New("connection refused")
				case test.untrusted:
					return fmt.Errorf("reach %s: %w", url, errTrustStore)
				}
				return nil
			}

			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

// TestProbeReachTrustStore covers the state scripts/install.sh runs the
// preflight in: a minimal rootfs with no ca-certificates, where every https
// endpoint fails verification even though the network is fine.
func TestProbeReachTrustStore(t *testing.T) {
	untrusted := &url.Error{
		Op:  "Head",
		URL: "https://apt.postgresql.org/",
		Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
	}

	tests := []struct {
		name        string
		do          error
		dial        error
		wantTrust   bool
		wantFailure bool
		contains    string
	}{
		{name: "reachable", contains: ""},
		{
			name:        "no trust store but the host answers",
			do:          untrusted,
			wantTrust:   true,
			wantFailure: true,
			contains:    "no trust store yet",
		},
		{
			name:        "offline hosts still fail",
			do:          untrusted,
			dial:        errors.New("network is unreachable"),
			wantFailure: true,
			contains:    "network is unreachable",
		},
		{
			name:        "an ordinary transport failure is untouched",
			do:          errors.New("connection refused"),
			wantFailure: true,
			contains:    "connection refused",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dialed string
			p := probe{
				do: func(*http.Request) (*http.Response, error) {
					if test.do != nil {
						return nil, test.do
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
				},
				dial: func(_ context.Context, address string) error {
					dialed = address
					return test.dial
				},
			}

			err := p.reach(context.Background(), "https://apt.postgresql.org/pub/repos/apt/dists/noble-pgdg/Release")
			if (err != nil) != test.wantFailure {
				t.Fatalf("err = %v, want failure %t", err, test.wantFailure)
			}
			if err != nil && !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("err %v does not contain %q", err, test.contains)
			}
			if errors.Is(err, errTrustStore) != test.wantTrust {
				t.Fatalf("errors.Is(%v, errTrustStore) = %t, want %t", err, !test.wantTrust, test.wantTrust)
			}
			if test.do == untrusted && dialed != "apt.postgresql.org:443" {
				t.Fatalf("dialed %q, want apt.postgresql.org:443", dialed)
			}
		})
	}
}

func TestPlatformCheck(t *testing.T) {
	ubuntu := "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n"

	tests := []struct {
		name      string
		osRelease string
		err       error
		goos      string
		arch      string
		want      Status
		contains  string
	}{
		{name: "supported", osRelease: ubuntu, goos: "linux", arch: "amd64", want: StatusOK, contains: "Ubuntu 24.04.1 LTS (noble), amd64"},
		{name: "arm64 is supported too", osRelease: ubuntu, goos: "linux", arch: "arm64", want: StatusOK, contains: "arm64"},
		{name: "wrong operating system", osRelease: ubuntu, goos: "darwin", arch: "arm64", want: StatusBlocker, contains: "darwin"},
		{name: "unsupported architecture", osRelease: ubuntu, goos: "linux", arch: "386", want: StatusBlocker, contains: "386"},
		{
			name:      "another distribution",
			osRelease: "ID=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\nPRETTY_NAME=\"Debian GNU/Linux 12\"\n",
			goos:      "linux", arch: "amd64", want: StatusBlocker, contains: "Debian",
		},
		{
			name:      "wrong Ubuntu release",
			osRelease: "ID=ubuntu\nVERSION_ID=\"22.04\"\nVERSION_CODENAME=jammy\nPRETTY_NAME=\"Ubuntu 22.04 LTS\"\n",
			goos:      "linux", arch: "amd64", want: StatusBlocker, contains: "22.04",
		},
		{
			name:      "no codename for the repositories",
			osRelease: "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
			goos:      "linux", arch: "amd64", want: StatusBlocker, contains: "VERSION_CODENAME",
		},
		{name: "unidentifiable host", err: errors.New("missing"), goos: "linux", arch: "amd64", want: StatusBlocker, contains: "cannot identify"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := NewPlatformCheck()
			check.OS, check.Arch = test.goos, test.arch
			check.OSRelease = func() (map[string]string, error) {
				if test.err != nil {
					return nil, test.err
				}
				return parseOSRelease(strings.NewReader(test.osRelease))
			}

			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

func TestSystemdCheck(t *testing.T) {
	tests := []struct {
		name   string
		exists func(string) bool
		want   Status
	}{
		{name: "running as init", exists: existsIn("/run/systemd/system"), want: StatusOK},
		{name: "image build", exists: existsIn("/usr/lib/systemd/systemd"), want: StatusWarning},
		{name: "absent", exists: existsIn(), want: StatusBlocker},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := SystemdCheck{Exists: test.exists}
			if result := check.Run(context.Background()); result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
		})
	}
}

type stubReader struct {
	snapshot capacity.Snapshot
	err      error
}

func (r stubReader) Read(context.Context) (capacity.Snapshot, error) { return r.snapshot, r.err }

func TestMemoryCheck(t *testing.T) {
	tests := []struct {
		name     string
		reader   capacity.Reader
		want     Status
		contains string
	}{
		{
			name:     "enough memory",
			reader:   stubReader{snapshot: capacity.Snapshot{Supported: true, TotalBytes: 8 * gib, AvailableBytes: 6 * gib, Profile: capacity.ProfilePro}},
			want:     StatusOK,
			contains: "8.0 GiB total, 6.0 GiB available, profile pro",
		},
		{
			// A stock 2 GB cloud VM: firmware and the kernel take their share of
			// MemTotal, so capacity.Classify rates it unsupported. Refusing it
			// would refuse the documented minimum spec.
			name:     "a 2 GB cloud VM installs",
			reader:   stubReader{snapshot: capacity.Snapshot{Supported: true, TotalBytes: 2084483072, AvailableBytes: 1900 * 1024 * 1024, Profile: capacity.Classify(2084483072)}},
			want:     StatusWarning,
			contains: "1.9 GiB total",
		},
		{
			name:     "under the supported floor",
			reader:   stubReader{snapshot: capacity.Snapshot{Supported: true, TotalBytes: gib, AvailableBytes: gib / 2, Profile: capacity.ProfileUnsupported}},
			want:     StatusBlocker,
			contains: "profile unsupported",
		},
		{
			name:     "unreadable",
			reader:   stubReader{err: errors.New("no /proc/meminfo")},
			want:     StatusWarning,
			contains: "unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := MemoryCheck{Reader: test.reader}
			result := check.Run(context.Background())
			if result.Status != test.want {
				t.Fatalf("status = %q, want %q (%s)", result.Status, test.want, result.Detail)
			}
			if !strings.Contains(result.Detail, test.contains) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.contains)
			}
		})
	}
}

func TestPodmanCheck(t *testing.T) {
	available := PodmanCheck{Inspect: func(context.Context) (podman.Status, error) {
		return podman.Status{Available: true, Version: "4.9.3", Path: "/usr/bin/podman"}, nil
	}}
	if result := available.Run(context.Background()); result.Status != StatusOK || result.Detail != "4.9.3 at /usr/bin/podman" {
		t.Fatalf("unexpected result: %+v", result)
	}

	missing := PodmanCheck{Inspect: func(context.Context) (podman.Status, error) {
		return podman.Status{}, podman.ErrUnavailable
	}}
	if result := missing.Run(context.Background()); result.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", result.Status, StatusWarning)
	}
}

func TestReportBlockers(t *testing.T) {
	report := Report{Checks: []Result{
		ok("a", "A", "fine"),
		warning("b", "B", "iffy", "look at it"),
		blocker("c", "C", "broken", "fix it"),
	}}
	if !report.HasBlocker() || !report.HasWarning() {
		t.Fatal("report should report both a blocker and a warning")
	}
	if blockers := report.Blockers(); len(blockers) != 1 || blockers[0].Name != "c" {
		t.Fatalf("unexpected blockers: %+v", blockers)
	}

	clean := Report{Checks: []Result{ok("a", "A", "fine")}}
	if clean.HasBlocker() || clean.HasWarning() {
		t.Fatal("a clean report should have neither blockers nor warnings")
	}
}

// TestNetworkCheckSeparatesAnExhaustedBudgetFromAnOfflineHost covers the report
// running out of time before a required endpoint answered. Nothing was
// observed about the network, so this must not read as "offline" — that would
// abort the install on a healthy but slow host.
func TestNetworkCheckSeparatesAnExhaustedBudgetFromAnOfflineHost(t *testing.T) {
	endpoints := []Endpoint{{Label: "Ubuntu archive", Host: "archive.ubuntu.com", URL: "http://archive.ubuntu.com/", Required: true}}

	spent, cancel := context.WithCancel(context.Background())
	cancel()

	check := NewNetworkCheck(endpoints)
	check.Resolve = func(ctx context.Context, host string) error {
		return fmt.Errorf("resolve %s: %w", host, ctx.Err())
	}
	check.Reach = func(context.Context, string) error { return nil }

	result := check.Run(spent)
	if result.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", result.Status, StatusWarning, result.Detail)
	}
	if !strings.Contains(result.Remediation, "ran out of time") {
		t.Fatalf("remediation does not name the budget as the reason: %s", result.Remediation)
	}
	if strings.Contains(result.Remediation, "an offline install cannot proceed") {
		t.Fatalf("remediation still claims the host is offline: %s", result.Remediation)
	}

	// Same failure with time left on the clock is a real observation, and a
	// genuinely offline host has to keep blocking the install.
	if result := check.Run(context.Background()); result.Status != StatusBlocker {
		t.Fatalf("status = %q, want %q (%s)", result.Status, StatusBlocker, result.Detail)
	}
}

// TestProbeReachAcceptsAnyStatus pins reachability as "the host answered". A
// mirror that returns 403 or 405 to a HEAD probe is online.
func TestProbeReachAcceptsAnyStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotFound, http.StatusBadGateway} {
		p := probe{
			do: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(""))}, nil
			},
			dial: func(context.Context, string) error { return errors.New("must not dial: the endpoint answered") },
		}
		if err := p.reach(context.Background(), "https://apt.postgresql.org/"); err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
	}

	// A transport failure is still a failure: nothing answered.
	p := probe{
		do:   func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") },
		dial: func(context.Context, string) error { return nil },
	}
	if err := p.reach(context.Background(), "https://apt.postgresql.org/"); err == nil {
		t.Fatal("a transport failure must not count as reachable")
	}
}

type budgetCheck struct {
	name      string
	block     bool
	remaining chan time.Duration
}

func (c budgetCheck) Name() string { return c.name }

func (c budgetCheck) Run(ctx context.Context) Result {
	deadline, _ := ctx.Deadline()
	c.remaining <- time.Until(deadline)
	if c.block {
		<-ctx.Done()
	}
	return ok(c.name, c.name, "done")
}

// TestRunDoesNotLetOneCheckStarveAnother is the budget regression: run in
// sequence, a check that burns the whole deadline leaves the checks behind it
// with nothing, and the network check — which runs last — then reports a
// healthy host as unreachable.
func TestRunDoesNotLetOneCheckStarveAnother(t *testing.T) {
	const budget = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	remaining := make(chan time.Duration, 2)
	results := Run(ctx, []Check{
		budgetCheck{name: "slow", block: true, remaining: remaining},
		budgetCheck{name: "network", remaining: remaining},
	})

	if len(results) != 2 || results[0].Name != "slow" || results[1].Name != "network" {
		t.Fatalf("results are not in check order: %+v", results)
	}
	for range results {
		if left := <-remaining; left < budget/2 {
			t.Fatalf("a check was left %s of a %s budget", left, budget)
		}
	}
}
