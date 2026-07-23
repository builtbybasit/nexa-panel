package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These guard the OPS-001 release gate against the failure mode it was written
// for: a lifecycle scenario that quietly stops executing. A deleted CI step or
// a dropped matrix entry does not fail any other test — the suite simply gets
// faster — so the wiring itself is asserted here.

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", relative))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestCIExecutesTheHostLifecycleScenarios(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(workflow, "scripts/test-node-lifecycle.sh") {
		t.Fatal("CI does not execute the host lifecycle scenarios; source-text contracts are not a substitute")
	}
	if !strings.Contains(workflow, "scripts/test-node-acceptance.sh") {
		t.Fatal("CI no longer runs the fresh-node acceptance checks")
	}
}

func TestCIEnablesBothDestructiveDatabaseSuites(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(workflow, "scripts/test-db-acceptance.sh") {
		t.Fatal("CI never runs the destructive database acceptance suites, so both skip")
	}
	for _, suite := range []string{"suite: mysql", "suite: postgres"} {
		if !strings.Contains(workflow, suite) {
			t.Fatalf("the database acceptance matrix is missing %q", suite)
		}
	}

	// The suites skip themselves unless these are set, and the script is the
	// only sanctioned place that sets them. If that moves, the CI job silently
	// turns into a no-op that still reports green.
	script := readRepoFile(t, "scripts/test-db-acceptance.sh")
	for _, gate := range []string{"NEXA_MYSQL_INTEGRATION=1", "NEXA_POSTGRES_INTEGRATION=1"} {
		if !strings.Contains(script, gate) {
			t.Fatalf("the acceptance script no longer opens the %s gate", gate)
		}
	}
}

func TestCIRunsEveryExecutedScenarioOnARM64(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(workflow, "ubuntu-24.04-arm") {
		t.Fatal("no ARM64 runner in CI; the release gate requires both architectures")
	}
	// One ARM64 entry somewhere is not enough: both runtime jobs are gated on
	// ARM64, so count the pairings rather than the string.
	if got := strings.Count(workflow, "runner: ubuntu-24.04-arm"); got < 3 {
		t.Fatalf("only %d ARM64 matrix entries; expected the lifecycle job plus both database suites", got)
	}
	if got := strings.Count(workflow, "runner: ubuntu-24.04"); got < 6 {
		t.Fatalf("only %d runner pairings in total; an architecture has been dropped", got)
	}
}

// A scenario nobody can run is still part of the gate. It must be visible in the
// checks list as a named, skipped job carrying its reason — never silently
// missing, which is indistinguishable from "covered" to a reader of a green run.
func TestUncoveredScenariosAreVisiblySkippedMatrixEntries(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(workflow, "vm-lifecycle:") {
		t.Fatal("the uncovered VM scenarios have no matrix job; a missing scenario reads as a passing one")
	}
	if !strings.Contains(workflow, "if: ${{ false }}") {
		t.Fatal("the VM matrix is not gated off, so CI would report green without any of its preconditions")
	}
	// Each entry must name itself and say why it did not run, on both
	// architectures the release gate requires.
	for _, scenario := range []string{
		"scenario: fresh-tls",
		"scenario: reinstall",
		"scenario: reboot",
		"scenario: update",
		"scenario: update-failure",
		"scenario: offline-rollback",
	} {
		if got := strings.Count(workflow, scenario+"\n"); got < 2 {
			t.Fatalf("%q appears in %d matrix entries; expected one per architecture", scenario, got)
		}
	}
	if got := strings.Count(workflow, "reason:"); got < 12 {
		t.Fatalf("only %d skipped entries state a reason; every uncovered scenario must say why", got)
	}
	if !strings.Contains(workflow, "${{ matrix.reason }}") {
		t.Fatal("the skipped jobs do not render their reason in the job name, where a reader would see it")
	}
}

// `all` is what an operator actually types on the throwaway server. Without it
// the VM matrix is five separate stateful invocations nobody will sequence
// correctly, and there is no single pass/fail answer at the end.
func TestVMEntryPointRunsTheWholeMatrixWithNumberedResults(t *testing.T) {
	script := readRepoFile(t, "scripts/test-vm-lifecycle.sh")
	for _, required := range []string{
		"scenario_all()",
		"ALL_PLAN=(",
		"print_summary()",
		"--resume",
		"--hostname",
		"--tls-email",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("the VM entry point cannot be driven end to end by an operator: missing %q", required)
		}
	}
	// A run that prints results but always exits 0 is worse than no run at all.
	if !strings.Contains(script, `printf '%d. %-8s %s\n'`) {
		t.Fatal("the summary does not print a numbered result per scenario")
	}
	if !strings.Contains(script, `[[ "$failed" -eq 0 && "$skipped" -eq 0 ]]`) {
		t.Fatal("the summary does not fail the run when a scenario failed or never ran")
	}
	// It reboots and reinstalls the host it runs on.
	if !strings.Contains(script, `"destroy this host"`) {
		t.Fatal("the whole-matrix run does not confirm before destroying the host")
	}
}

func TestVMOnlyScenariosAreImplementedAndNotFakedInCI(t *testing.T) {
	script := readRepoFile(t, "scripts/test-vm-lifecycle.sh")
	for _, scenario := range []string{
		"scenario_fresh_tls",
		"scenario_reinstall",
		"scenario_reboot",
		"scenario_update",
		"scenario_update_failure",
		"scenario_offline_rollback",
	} {
		if !strings.Contains(script, scenario+"()") {
			t.Fatalf("the VM entry point does not implement %s", scenario)
		}
	}
	// The fresh-TLS scenario is worthless if it accepts a certificate the
	// installer could have produced on its own.
	for _, proof := range []string{
		`openssl x509 -in "$chain" -noout -issuer`,
		"the installed certificate is self-signed",
	} {
		if !strings.Contains(script, proof) {
			t.Fatalf("the fresh TLS scenario does not prove the certificate is real: missing %q", proof)
		}
	}

	// Wiring this into GitHub-hosted CI would report green without any of the
	// infrastructure the scenarios need. It stays out until a VM runner exists.
	// Referring to the script in a comment is fine; running it is not.
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	for _, line := range strings.Split(workflow, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		if strings.Contains(code, "test-vm-lifecycle.sh") {
			t.Fatalf("the VM-only scenarios are invoked by CI, where none of their preconditions hold: %s", code)
		}
	}
}

// The transaction journal records a bare semantic version ("0.3.0"); `nexa
// version` prints that version followed by the commit and build stamp. An
// assertion that compares one against the other can never hold, so the scenario
// fails on a node that rolled back perfectly — which is exactly what happened on
// the first live run of this matrix. Only the whole-line comparisons between two
// readings of the same source are safe.
func TestVersionAssertionsCompareLikeWithLike(t *testing.T) {
	script := readRepoFile(t, "scripts/test-vm-lifecycle.sh")
	if !strings.Contains(script, "installed_version_number()") {
		t.Fatal("no helper extracts the bare version, so a journal version cannot be compared with the installed one")
	}
	// The two sites that read a version out of the transaction journal.
	for _, comparison := range []string{
		`[[ "$(installed_version_number)" == "$rollback_target" ]]`,
	} {
		if !strings.Contains(script, comparison) {
			t.Fatalf("a journal version is not compared against the bare installed version: missing %q", comparison)
		}
	}
	// The stamped line is still what a human should read in the failure message.
	if !strings.Contains(script, `fail "the node reports $(installed_version) after rollback, not $rollback_target"`) {
		t.Fatal("the rollback failure message no longer reports the full stamped version it actually found")
	}
}

func TestLifecycleCoverageIsDocumented(t *testing.T) {
	doc := readRepoFile(t, "docs/ci-lifecycle.md")
	for _, uncovered := range []string{
		"fresh-tls",
		"update-failure",
		"offline-rollback",
		"Infrastructure still required",
	} {
		if !strings.Contains(doc, uncovered) {
			t.Fatalf("docs/ci-lifecycle.md does not account for %q", uncovered)
		}
	}
}
