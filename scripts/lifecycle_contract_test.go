package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readScript(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// The installer's interesting behaviour is what it does to a host, and a
// strings.Contains grep over the shell source has already let a real bug
// through. These tests therefore run the installer inside a disposable copy of
// the test node image and inspect the machine afterwards.
//
// The container is thrown away with the test, so injecting a failure into it is
// safe in a way that injecting one into the shared `nexa-node` never is.
const testNodeImage = "nexa-node"

func requireTestNodeImage(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell contract")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; the host-mutation tests need the test node image")
	}
	if output, err := exec.Command("docker", "image", "inspect", testNodeImage).CombinedOutput(); err != nil {
		t.Skipf("the %s image is not built (make node): %v\n%s", testNodeImage, err, output)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// runInDisposableNode executes script in a fresh container from the test node
// image with the repository mounted read-only at /repo, and returns its combined
// output and exit status. The script is responsible for reporting what it found;
// nothing it does can reach the developer's node.
func runInDisposableNode(t *testing.T, script string) (string, int) {
	t.Helper()
	root := requireTestNodeImage(t)
	command := exec.Command("docker", "run", "--rm", "-v", root+":/repo:ro", testNodeImage, "bash", "-c", script)
	output, err := command.CombinedOutput()
	status := 0
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		status = exitError.ExitCode()
	} else if err != nil {
		t.Fatalf("run in %s: %v\n%s", testNodeImage, err, output)
	}
	return string(output), status
}

// The installer tree, copied out of the read-only mount so a test can inject a
// failure into the packaging it installs from.
const stageInstaller = `mkdir -p /tmp/staged && cp -a /repo/scripts /repo/packaging /tmp/staged/`

// Everything the installer is allowed to touch, fingerprinted by path, size,
// mode, owner and modification time. A dry run must not move any of it.
const hostFingerprint = `{
  find /etc /usr/lib/systemd/system /usr/lib/sysusers.d /usr/lib/tmpfiles.d \
       /usr/lib/nexa-panel /usr/sbin /var/lib/nexa-panel /var/log/nexa-panel /srv/nexa /run/sshd \
       -xdev \( -type f -o -type l -o -type d \) -printf '%p %s %m %u:%g %T@\n' 2>/dev/null | sort
  getent passwd nexa || true
  ls -1 /etc/apt/sources.list.d /etc/apt/trusted.gpg.d 2>/dev/null || true
} | md5sum`

func TestInstallerDryRunPlansTheWholeInstallAndChangesNothing(t *testing.T) {
	output, status := runInDisposableNode(t, stageInstaller+`
transcripts_before=$(ls -1 /var/log/nexa-panel-install.*.log 2>/dev/null | wc -l)
`+hostFingerprint+` > /tmp/before
/tmp/staged/scripts/install.sh --dry-run --allow-insecure-http
plan_status=$?
`+hostFingerprint+` > /tmp/after
echo "PLAN_STATUS=$plan_status"
if cmp -s /tmp/before /tmp/after; then echo HOST_UNCHANGED; else echo HOST_MUTATED; fi
transcripts_after=$(ls -1 /var/log/nexa-panel-install.*.log 2>/dev/null | wc -l)
[ "$transcripts_before" = "$transcripts_after" ] || echo TRANSCRIPT_WRITTEN
exit 0`)
	if status != 0 {
		t.Fatalf("dry run container exited %d:\n%s", status, output)
	}
	if !strings.Contains(output, "PLAN_STATUS=0") {
		t.Errorf("dry run did not succeed:\n%s", output)
	}
	if !strings.Contains(output, "HOST_UNCHANGED") {
		t.Errorf("a dry run mutated the host:\n%s", output)
	}
	if strings.Contains(output, "TRANSCRIPT_WRITTEN") {
		t.Errorf("a dry run wrote an install transcript:\n%s", output)
	}
	for _, planned := range []string{
		"Nexa Panel install plan (dry run)",
		"PUBLISH all interfaces on :8888 over plaintext HTTP",
		"INSTALL_PACKAGE nginx",
		"WRITE /usr/lib/systemd/system/nexa-api.service (mode 0644)",
		"WRITE /usr/lib/tmpfiles.d/nexa-panel.conf (mode 0644)",
		"WRITE /etc/systemd/system/nexa-api.service.d/10-nexa-panel.conf (mode 0644)",
		"SYMLINK /etc/nginx/sites-enabled/nexa-panel.conf -> /etc/nginx/sites-available/nexa-panel.conf",
		"ENABLE nexa-agent.service",
		"ENABLE nexa-update-recovery.service",
		"RUN nginx -t",
		"RUN sshd -t",
		"RECORD the publishing state in /etc/nexa-panel/publishing.json",
		"RETAIN hosted sites, databases, backups, panel state, and TLS material",
	} {
		if !strings.Contains(output, planned) {
			t.Errorf("dry-run plan omits %q:\n%s", planned, output)
		}
	}
}

func TestInstallerDryRunPlansFirewallCertificateAndIngressVerification(t *testing.T) {
	// The certificate/firewall path refuses a host without a running systemd or a
	// startable binary, so the plan is computed against a container that claims
	// to have both. Nothing below the plan runs, and the plan itself is pure
	// computation from the flags.
	output, status := runInDisposableNode(t, stageInstaller+`
mkdir -p /run/systemd/system
printf '#!/bin/sh\nexit 0\n' > /usr/bin/nexa && chmod 0755 /usr/bin/nexa
`+hostFingerprint+` > /tmp/before
/tmp/staged/scripts/install.sh --dry-run --panel-hostname panel.example.com --tls-email ops@example.com --manage-firewall
plan_status=$?
`+hostFingerprint+` > /tmp/after
echo "PLAN_STATUS=$plan_status"
if cmp -s /tmp/before /tmp/after; then echo HOST_UNCHANGED; else echo HOST_MUTATED; fi
exit 0`)
	if status != 0 {
		t.Fatalf("dry run container exited %d:\n%s", status, output)
	}
	if !strings.Contains(output, "PLAN_STATUS=0") || !strings.Contains(output, "HOST_UNCHANGED") {
		t.Fatalf("published dry run failed or mutated the host:\n%s", output)
	}
	for _, planned := range []string{
		"PUBLISH panel.example.com on :80 with TLS (certbot)",
		"RUN ufw allow 80/tcp comment 'Nexa Panel managed'",
		"RUN ufw allow 443/tcp comment 'Nexa Panel managed'",
		"RUN certbot --nginx --non-interactive --agree-tos --redirect --email ops@example.com -d panel.example.com",
		"VERIFY https://panel.example.com/api/v1/health/live",
	} {
		if !strings.Contains(output, planned) {
			t.Errorf("published dry-run plan omits %q:\n%s", planned, output)
		}
	}
}

// buildLinuxNexa compiles the real binary for the container's architecture. The
// publishing tests below need a nexa that can actually read the record: a stub
// that exits zero would prove only that the installer called something.
func buildLinuxNexa(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "nexa")
	build := exec.Command("go", "build", "-o", binary, "./cmd/nexa")
	build.Dir = ".."
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build a linux nexa for the container: %v\n%s", err, output)
	}
	return binary
}

// runInDisposableNodeWithNexa is runInDisposableNode with a working /usr/bin/nexa.
func runInDisposableNodeWithNexa(t *testing.T, script string) (string, int) {
	t.Helper()
	root := requireTestNodeImage(t)
	binary := buildLinuxNexa(t)
	command := exec.Command("docker", "run", "--rm",
		"-v", root+":/repo:ro",
		"-v", binary+":/usr/bin/nexa:ro",
		testNodeImage, "bash", "-c", script)
	output, err := command.CombinedOutput()
	status := 0
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		status = exitError.ExitCode()
	} else if err != nil {
		t.Fatalf("run in %s: %v\n%s", testNodeImage, err, output)
	}
	return string(output), status
}

// A TLS node that has been uninstalled with its data retained: /etc/nexa-panel
// survives, the panel vhost does not, and the certificate is still on disk.
const retainedTLSPublication = `
mkdir -p /etc/nexa-panel /run/systemd/system
cat > /etc/nexa-panel/publishing.json <<'JSON'
{
  "version": 1,
  "hostname": "panel.example.com",
  "port": 443,
  "tls": true,
  "externalTls": false,
  "updatedAt": "2026-07-01T00:00:00Z",
  "source": "install"
}
JSON
rm -f /etc/nginx/sites-available/nexa-panel.conf /etc/nginx/sites-enabled/nexa-panel.conf
`

// Defect 7. A retain-data uninstall removes the panel vhost, which used to be the
// only place the publishing mode existed. A flagless reinstall then saw a machine
// with nothing published, and republished a public HTTPS node on 127.0.0.1:8888 —
// retaining the certificate, using none of it, and reporting no error at all.
func TestReinstallOverARetainedRecordRepublishesOverHTTPSNotLoopback(t *testing.T) {
	output, status := runInDisposableNodeWithNexa(t, stageInstaller+retainedTLSPublication+`
mkdir -p /etc/letsencrypt/live/panel.example.com
printf 'not a real certificate\n' > /etc/letsencrypt/live/panel.example.com/fullchain.pem
/tmp/staged/scripts/install.sh --dry-run
echo "PLAN_STATUS=$?"
exit 0`)
	if status != 0 {
		t.Fatalf("reinstall dry run container exited %d:\n%s", status, output)
	}
	if !strings.Contains(output, "PLAN_STATUS=0") {
		t.Fatalf("a flagless reinstall over a retained publishing record did not plan:\n%s", output)
	}
	if strings.Contains(output, "PUBLISH loopback only") {
		t.Fatalf("a flagless reinstall downgraded a recorded HTTPS node to loopback:\n%s", output)
	}
	for _, planned := range []string{
		"PUBLISH preserved from /etc/nexa-panel/publishing.json: tls on panel.example.com",
		"RUN certbot install --nginx --cert-name panel.example.com --redirect",
		"VERIFY https://panel.example.com/api/v1/health/live",
	} {
		if !strings.Contains(output, planned) {
			t.Errorf("the reinstall plan omits %q:\n%s", planned, output)
		}
	}
}

// The failure this replaces was silent, so the replacement must not be. With the
// certificate gone there is no way to honour the recorded publication, and the
// only acceptable outcome is a refusal that names both remedies.
func TestReinstallRefusesWhenTheRecordedCertificateIsGone(t *testing.T) {
	output, status := runInDisposableNodeWithNexa(t, stageInstaller+retainedTLSPublication+`
rm -rf /etc/letsencrypt/live
/tmp/staged/scripts/install.sh --dry-run
echo "PLAN_STATUS=$?"
exit 0`)
	if status != 0 {
		t.Fatalf("container exited %d:\n%s", status, output)
	}
	if strings.Contains(output, "PLAN_STATUS=0") {
		t.Fatalf("a reinstall that cannot honour the recorded HTTPS publication reported success:\n%s", output)
	}
	if !strings.Contains(output, "recorded as published over HTTPS on panel.example.com") {
		t.Errorf("the refusal does not explain what it could not honour:\n%s", output)
	}
	for _, remedy := range []string{"--tls-email", "--allow-insecure-http"} {
		if !strings.Contains(output, remedy) {
			t.Errorf("the refusal does not name the %s way out:\n%s", remedy, output)
		}
	}
}

func TestFailedPackagingRefreshRestoresEveryManagedFile(t *testing.T) {
	// A real injected failure, not a test hook: the proxy snippet the installer
	// writes is made invalid, so `nginx -t` refuses it after several managed
	// files have already been replaced.
	output, status := runInDisposableNode(t, stageInstaller+`
printf '\nthis_is_not_an_nginx_directive;\n' >> /tmp/staged/packaging/nginx/nexa-panel-proxy.conf.template
printf '\n# injected\n' >> /tmp/staged/packaging/systemd/nexa-api.service
md5sum /etc/nginx/snippets/nexa-panel-proxy.conf /usr/lib/systemd/system/nexa-api.service \
       /etc/nginx/sites-available/nexa-panel.conf > /tmp/before
/tmp/staged/scripts/install.sh --sync-packaging --verbose
echo "INSTALL_STATUS=$?"
md5sum -c --quiet /tmp/before && echo MANAGED_FILES_RESTORED
grep -l injected /etc/nginx/snippets/nexa-panel-proxy.conf /usr/lib/systemd/system/nexa-api.service 2>/dev/null && echo INJECTION_REMAINS
readlink /etc/nginx/sites-enabled/nexa-panel.conf
nginx -t 2>&1 | tail -1
exit 0`)
	if status != 0 {
		t.Fatalf("rollback container exited %d:\n%s", status, output)
	}
	if strings.Contains(output, "INSTALL_STATUS=0") {
		t.Fatalf("a failed packaging refresh reported success:\n%s", output)
	}
	for _, expected := range []string{
		"Rolling back this run's changes, newest first.",
		"RESTORE /etc/nginx/snippets/nexa-panel-proxy.conf",
		"RESTORE /usr/lib/systemd/system/nexa-api.service",
		"MANAGED_FILES_RESTORED",
		"/etc/nginx/sites-available/nexa-panel.conf",
		"test is successful",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("rollback did not report/achieve %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "INJECTION_REMAINS") {
		t.Errorf("the injected content survived the rollback:\n%s", output)
	}
}

func TestFailedFreshInstallLeavesNoAccountManagedRootOrIngress(t *testing.T) {
	// The node is purged first so the installer runs as a genuine fresh install:
	// it creates the service account and all four managed roots, and the failure
	// has to take every one of them back out. An abandoned account or root is
	// precisely what makes the next attempt refuse to install.
	output, status := runInDisposableNode(t, stageInstaller+`
/tmp/staged/scripts/uninstall.sh --purge-data --yes > /tmp/uninstall.log 2>&1 || { echo PURGE_FAILED; tail -5 /tmp/uninstall.log; exit 0; }
printf '\nthis_is_not_an_nginx_directive;\n' >> /tmp/staged/packaging/nginx/nexa-panel-proxy.conf.template
/tmp/staged/scripts/install.sh --allow-insecure-http --skip-preflight --verbose
echo "INSTALL_STATUS=$?"
getent passwd nexa >/dev/null && echo LEFTOVER_ACCOUNT
for path in /var/lib/nexa-panel /etc/nexa-panel /var/log/nexa-panel /srv/nexa \
            /etc/nginx/snippets/nexa-panel-proxy.conf /etc/nginx/sites-available/nexa-panel.conf \
            /usr/lib/systemd/system/nexa-api.service /usr/lib/tmpfiles.d/nexa-panel.conf \
            /etc/systemd/system/nexa-api.service.d; do
  [ -e "$path" ] && echo "LEFTOVER $path"
done
nginx -t 2>&1 | tail -1
sshd -t && echo SSHD_VALID
exit 0`)
	if status != 0 {
		t.Fatalf("fresh install container exited %d:\n%s", status, output)
	}
	if strings.Contains(output, "PURGE_FAILED") {
		t.Fatalf("could not purge the node before the fresh install:\n%s", output)
	}
	if strings.Contains(output, "INSTALL_STATUS=0") {
		t.Fatalf("a failed fresh install reported success:\n%s", output)
	}
	if strings.Contains(output, "LEFTOVER") {
		t.Errorf("a failed fresh install left mutations behind:\n%s", output)
	}
	for _, expected := range []string{
		"REMOVE_SERVICE_ACCOUNT nexa",
		"RETAIN the Ubuntu packages installed by this run",
		"test is successful",
		"SSHD_VALID",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("failed fresh install did not report %q:\n%s", expected, output)
		}
	}
}

func TestSuccessfulInstallStillCompletesAndDiscardsItsJournal(t *testing.T) {
	// The counterpart to the injected-failure tests: the journalling must not
	// change what a working install does, and a run that succeeded must leave no
	// journal or saved original behind.
	output, status := runInDisposableNode(t, stageInstaller+`
/tmp/staged/scripts/uninstall.sh --purge-data --yes > /tmp/uninstall.log 2>&1 || { echo PURGE_FAILED; exit 0; }
/tmp/staged/scripts/install.sh --no-start --allow-insecure-http --skip-preflight > /tmp/install.log 2>&1
echo "INSTALL_STATUS=$?"
tail -3 /tmp/install.log
getent passwd nexa >/dev/null || echo MISSING_ACCOUNT
for path in /var/lib/nexa-panel/install/ownership.v1 /etc/nginx/sites-enabled/nexa-panel.conf \
            /usr/lib/systemd/system/nexa-api.service /usr/sbin/nexa-uninstall; do
  [ -e "$path" ] || echo "MISSING $path"
done
find /tmp -maxdepth 2 -name 'rollback.journal' | head -1
nginx -t 2>&1 | tail -1
exit 0`)
	if status != 0 {
		t.Fatalf("install container exited %d:\n%s", status, output)
	}
	if strings.Contains(output, "PURGE_FAILED") {
		t.Fatalf("could not purge the node before reinstalling:\n%s", output)
	}
	if !strings.Contains(output, "INSTALL_STATUS=0") {
		t.Fatalf("a clean install failed:\n%s", output)
	}
	if strings.Contains(output, "MISSING") {
		t.Errorf("a successful install did not produce its own layout:\n%s", output)
	}
	if strings.Contains(output, "rollback.journal") {
		t.Errorf("a successful install left its rollback journal behind:\n%s", output)
	}
	if !strings.Contains(output, "test is successful") {
		t.Errorf("Nginx does not validate after a clean install:\n%s", output)
	}
}

func TestIngressVerificationFailsWhenThePanelDoesNotAnswer(t *testing.T) {
	// Nginx is installed in the image but not running here, so the published
	// listener answers nothing at all — exactly the state a successful-looking
	// install must never be reported from.
	output, status := runInDisposableNode(t, stageInstaller+`
NEXA_INGRESS_ATTEMPTS=1 NEXA_INGRESS_DELAY=0 /tmp/staged/scripts/install.sh --verify-ingress
echo "VERIFY_STATUS=$?"
exit 0`)
	if status != 0 {
		t.Fatalf("verification container exited %d:\n%s", status, output)
	}
	if strings.Contains(output, "VERIFY_STATUS=0") {
		t.Fatalf("ingress verification passed against a panel that answers nothing:\n%s", output)
	}
	if !strings.Contains(output, "not reachable through its published listener") {
		t.Errorf("ingress failure does not explain itself:\n%s", output)
	}
}

func TestIngressVerificationPassesAgainstTheRunningNode(t *testing.T) {
	requireTestNodeImage(t)
	if output, err := exec.Command("docker", "exec", "nexa-node", "true").CombinedOutput(); err != nil {
		t.Skipf("the nexa-node container is not running: %v\n%s", err, output)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), "install.sh")
	if err := os.Link(filepath.Join(root, "scripts", "install.sh"), staged); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("docker", "cp", staged, "nexa-node:/tmp/nexa-verify-ingress.sh").CombinedOutput(); err != nil {
		t.Fatalf("stage the installer: %v\n%s", err, output)
	}
	output, err := exec.Command("docker", "exec", "nexa-node", "bash", "/tmp/nexa-verify-ingress.sh", "--verify-ingress").CombinedOutput()
	if err != nil {
		t.Fatalf("the live node's published listener did not answer: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "answered HTTP 200") {
		t.Errorf("ingress verification did not confirm a 200:\n%s", output)
	}
}

func TestInstallerVerifiesPublicIngressBeforeReportingSuccess(t *testing.T) {
	content := readScript(t, "install.sh")
	verify := strings.Index(content, `verify_public_ingress "$(planned_panel_url)"`)
	seed := strings.Index(content, `bash "$SEED_SCRIPT" "$panel_url"`)
	if verify < 0 || seed < 0 {
		t.Fatal("installer no longer verifies public ingress or seeds the administrator")
	}
	if verify > seed {
		t.Fatal("the installer prints administrator credentials before proving the panel can be reached")
	}
	if !strings.Contains(content, "/api/v1/health/live") {
		t.Fatal("ingress verification does not fetch the panel through its listener")
	}
}

func TestRollbackJournalIsWrittenBeforeTheMutationItUndoes(t *testing.T) {
	content := readScript(t, "install.sh")
	for _, required := range []string{
		"journal_path \"$destination\"",
		"journal identity",
		"journal ufw_added",
		"journal certificate",
		"journal_path /usr/bin/nexa",
		"journal_path /etc/ssh/sshd_config",
		"journal_unit_enablement",
		"journal_service_state",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("installer does not journal %q", required)
		}
	}
	// A privilege repair is deliberately never undone.
	if !strings.Contains(content, "journal security_repair www-data nexa") {
		t.Error("removing www-data from the privileged nexa group is not recorded as an unreversed repair")
	}
	if strings.Contains(content, "gpasswd -a www-data nexa") {
		t.Fatal("rollback re-grants the web server access to the privileged agent group")
	}
}

func TestInstallerValidatesBeforeInstallingBinary(t *testing.T) {
	content := readScript(t, "install.sh")
	preflight := strings.Index(content, "# --- preflight")
	installBinary := strings.Index(content, "# --- install binary")
	if preflight < 0 || installBinary < 0 {
		t.Fatalf("installer is missing lifecycle phase markers")
	}
	if preflight > installBinary {
		t.Fatal("installer mutates /usr/bin before preflight succeeds")
	}
}

func TestInstallerRequiresExplicitExposureAndFirewallConsent(t *testing.T) {
	content := readScript(t, "install.sh")
	if strings.Contains(content, "usermod -a -G nexa www-data") {
		t.Fatal("installer gives the web server access to the privileged agent group")
	}
	for _, required := range []string{
		"--allow-insecure-http",
		"--manage-firewall",
		"loopback",
		"MANAGE_FIREWALL",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("installer is missing the explicit exposure contract %q", required)
		}
	}
	if strings.Contains(content, "ufw --force enable") {
		t.Fatal("installer must never enable a host firewall as a side effect")
	}
}

// The stock Ubuntu nginx (1.24) has no QUIC, so a site with HTTP/3 enabled fails
// `nginx -t` and rolls back. The installer provisions an HTTP/3-capable build
// from nginx.org's stable repository, which it must configure BEFORE the
// prerequisite `nginx` package is installed (otherwise apt pulls the 1.24 archive
// build), then reconcile to the panel's layout: the worker user back to www-data
// (nginx.org ships `user nginx;`, which cannot read the www-data FPM sockets or
// site trees) and a sites-enabled include (nginx.org's nginx.conf loads only
// conf.d/*.conf).
func TestInstallerProvisionsHTTP3CapableNginx(t *testing.T) {
	content := readScript(t, "install.sh")
	repo := strings.Index(content, "nginx.org/packages/ubuntu")
	prereqInstall := strings.Index(content, `apt-get install -y --no-install-recommends "${PREREQUISITE_PACKAGES[@]}"`)
	if repo < 0 || prereqInstall < 0 {
		t.Fatal("installer no longer configures the nginx repository or installs the prerequisites")
	}
	if repo > prereqInstall {
		t.Fatal("nginx.org repository is configured after nginx is installed; apt would pull Ubuntu's 1.24 build")
	}
	// Security-relevant: a worker running as nginx cannot reach the www-data FPM
	// sockets, so the reconciliation must force the worker user back to www-data.
	if !strings.Contains(content, "user  www-data;") {
		t.Error("installer does not force the nginx worker user to www-data")
	}
	// The repo files and the reconciliation edit must be rollback-safe, and the
	// sites-enabled include must sort last among conf.d/*.conf (zzz- prefix) so
	// every per-site limit_req_zone is defined before the vhost that uses it.
	for _, required := range []string{
		"journal_path /usr/share/keyrings/nginx-archive-keyring.gpg",
		"journal_path /etc/apt/sources.list.d/nginx.list",
		"journal_path /etc/nginx/nginx.conf",
		"journal_path /etc/nginx/conf.d/zzz-nexa-sites-enabled.conf",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("installer does not journal %q for rollback", required)
		}
	}
	// A purge must remove the include drop-in the installer created.
	if !strings.Contains(readScript(t, "uninstall.sh"), "/etc/nginx/conf.d/zzz-nexa-sites-enabled.conf") {
		t.Error("uninstall does not remove the sites-enabled include drop-in")
	}
}

func TestDownloadHandsOffBeforeLifecycleLockAcquisition(t *testing.T) {
	content := readScript(t, "install.sh")
	handoff := strings.Index(content, "if [[ \"$DOWNLOAD\" -eq 1 ]]")
	lock := strings.Index(content, "if [[ -n \"${NEXA_LIFECYCLE_LOCK_FD:-}\" ]]")
	if handoff < 0 || lock < 0 {
		t.Fatal("installer is missing the download handoff or lifecycle lock contract")
	}
	if lock < handoff {
		t.Fatal("download parent acquires the lifecycle lock before starting the unpacked installer")
	}
	for _, proof := range []string{"/proc/$$/fd/$NEXA_LIFECYCLE_LOCK_FD", `-ef "$LIFECYCLE_LOCK_PATH"`} {
		if !strings.Contains(content, proof) {
			t.Fatalf("installer accepts inherited update lock without proof %q", proof)
		}
	}
}

func TestPackagingSyncNeverAutoSelectsOrSwapsBundledBinary(t *testing.T) {
	content := readScript(t, "install.sh")
	if !strings.Contains(content, `[[ "$MODE" == "install" && -z "$BINARY" && -f "$ROOT_DIR/bin/nexa" ]]`) {
		t.Fatal("release bundle auto-selects its binary during packaging-only sync")
	}
	if !strings.Contains(content, "--sync-packaging never swaps the executable") {
		t.Fatal("installer does not reject an explicit binary during packaging-only sync")
	}
}

func TestInstallerRecordsAndRequiresManagedRootOwnership(t *testing.T) {
	content := readScript(t, "install.sh")
	for _, required := range []string{
		"ownership.v1",
		"--adopt-existing",
		"validate_service_identity",
		"managed root $path is not a real directory",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("installer ownership collision contract is missing %q", required)
		}
	}
}

func TestReleaseHelperParsesRealJSONIndependentOfFormatting(t *testing.T) {
	metadata := map[string]any{
		"assets": []any{
			map[string]any{"url": "https://api.github.com/repos/o/r/releases/assets/42", "name": "nexa-panel-linux-arm64.tar.gz"},
		},
	}
	encoded, err := json.MarshalIndent(metadata, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(metadataPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "nexa-release-helper.py", "asset-url", metadataPath, "nexa-panel-linux-arm64.tar.gz")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve asset: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "https://api.github.com/repos/o/r/releases/assets/42" {
		t.Fatalf("asset URL = %q", got)
	}
}

func TestReleaseHelperRejectsTraversalArchive(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "release.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("owned")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "unpacked")
	command := exec.Command("python3", "nexa-release-helper.py", "extract", archivePath, destination)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("unsafe archive was accepted:\n%s", output)
	}
}

func TestReleaseHelperMigratesLegacyTLSVhostWithoutRewritingCertificates(t *testing.T) {
	legacy := `limit_req_zone $binary_remote_addr zone=nexa_auth:10m rate=10r/m;
server {
    server_name panel.example.com;
    listen 443 ssl; # managed by Certbot
    ssl_certificate /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;
    limit_req_status 429;
    client_max_body_size 10m;
    client_body_timeout 5m;
    location = /metrics {
        proxy_pass http://unix:/run/nexa-panel/api.sock;
    }
    location /api/v1/auth/ {
        proxy_pass http://unix:/run/nexa-panel/api.sock;
    }
    location / {
        proxy_pass http://unix:/run/nexa-panel/api.sock;
    }
}
server {
    listen 80;
    server_name panel.example.com;
    return 301 https://$host$request_uri;
}
`
	directory := t.TempDir()
	input := filepath.Join(directory, "legacy.conf")
	output := filepath.Join(directory, "migrated.conf")
	if err := os.WriteFile(input, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "nexa-release-helper.py", "migrate-nginx-vhost", input, output)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("migrate vhost: %v\n%s", err, result)
	}
	migrated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(migrated)
	for _, preserved := range []string{
		"ssl_certificate /etc/letsencrypt/live/panel.example.com/fullchain.pem;",
		"return 301 https://$host$request_uri;",
		"include /etc/nginx/snippets/nexa-panel-proxy.conf;",
	} {
		if !strings.Contains(text, preserved) {
			t.Errorf("migrated vhost lost %q:\n%s", preserved, text)
		}
	}
	if strings.Contains(text, "proxy_pass http://unix:/run/nexa-panel/api.sock") {
		t.Fatalf("legacy proxy locations remain alongside the managed include:\n%s", text)
	}
}

func TestSeedAdminReadinessFailureIsFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell contract")
	}
	dir := t.TempDir()
	curl := filepath.Join(dir, "curl")
	if err := os.WriteFile(curl, []byte("#!/bin/sh\nexit 22\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "nexa-seed-admin.sh", "http://localhost/")
	command.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"NEXA_SEED_READY_ATTEMPTS=1",
		"NEXA_SEED_READY_DELAY=0",
	)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("seed helper reported success when readiness failed:\n%s", output)
	}
}

func TestUninstallDefaultsToRetainingCustomerData(t *testing.T) {
	command := exec.Command("bash", "uninstall.sh", "--dry-run")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run uninstall: %v\n%s", err, output)
	}
	text := string(output)
	// The publishing record is the one retained file that decides what a later
	// reinstall does. The panel vhost is removed by every uninstall, so without
	// the record a reinstall has nothing left to learn the hostname and TLS mode
	// from and silently republishes a public HTTPS node on loopback.
	for _, retained := range []string{"/var/lib/nexa-panel", "/srv/nexa/sites", "/var/lib/postgresql", "/etc/nexa-panel/publishing.json"} {
		if !strings.Contains(text, "RETAIN "+retained) {
			t.Errorf("default uninstall does not promise to retain %s:\n%s", retained, text)
		}
	}
	if strings.Contains(text, "REMOVE /var/lib/nexa-panel") || strings.Contains(text, "REMOVE /srv/nexa/sites") {
		t.Fatalf("default uninstall would delete customer data:\n%s", text)
	}
}

func TestUninstallRequiresConfirmationToPurgeData(t *testing.T) {
	command := exec.Command("bash", "uninstall.sh", "--purge-data")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err == nil {
		t.Fatalf("purge ran without --yes:\n%s", output.String())
	}

	command = exec.Command("bash", "uninstall.sh", "--dry-run", "--purge-data")
	output.Reset()
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("purge dry-run: %v\n%s", err, output.String())
	}
	for _, removed := range []string{"/var/lib/nexa-panel", "/srv/nexa/sites"} {
		if !strings.Contains(output.String(), "REMOVE "+removed) {
			t.Errorf("purge plan omits %s:\n%s", removed, output.String())
		}
	}
}

func TestPurgeDeletesOnlyVerifiedManagedAccountsAndConfigurations(t *testing.T) {
	content := readScript(t, "uninstall.sh")
	if strings.Contains(content, "done < <(getent passwd)") {
		t.Fatal("uninstaller scans every account and deletes by a broad prefix")
	}
	for _, required := range []string{
		`found_home" == "$site_root`,
		"lacks the Nexa Panel ownership header",
		"REMOVE_MANAGED_SITE_ACCOUNT",
		"Keep the recovery command available until every fallible",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("uninstaller safe purge contract is missing %q", required)
		}
	}
}
