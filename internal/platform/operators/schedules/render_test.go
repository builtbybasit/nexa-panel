package schedules

import (
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

const testTaskID = "0123456789abcdef0123456789abcdef"

func testTask() Task {
	return Task{
		ID: testTaskID, Name: "Nightly report", CronExpression: "*/5 1-6 * * 1",
		Command: `echo 'hello' && printf '%s' "done"`, TimeoutSeconds: 120, Enabled: true,
		Scope: sitefs.Scope{SiteID: "site_1", Slug: "demo", RootPath: "/srv/nexa/sites/demo", UnixUser: "nexa_demo"},
	}
}

// The wrapper contract is pinned byte for byte: overlap skipping via flock,
// a hard timeout, TSV run records, and bounded log trimming. The embedded
// command contains single quotes to prove the quote escaping.
const expectedWrapper = `#!/bin/sh
# Managed by Nexa Panel. Task Nightly report (0123456789abcdef0123456789abcdef) for site demo. Runs as nexa_demo.
set -u
TRIGGER="${1:-manual}"
ROOT='/srv/nexa/sites/demo'
LOG="$ROOT/logs/tasks/0123456789abcdef0123456789abcdef.log"
RUNS="$ROOT/logs/tasks/0123456789abcdef0123456789abcdef.runs"
mkdir -p "$ROOT/logs/tasks" || exit 111
exec 9>>"$ROOT/tmp/.nexa-task-0123456789abcdef0123456789abcdef.lock" || exit 111
if ! flock -n 9; then
  printf '%s\t0\tskipped-overlap\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$TRIGGER" >>"$RUNS"
  exit 0
fi
START_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; START_S="$(date +%s)"
cd "$ROOT" || exit 111
timeout -k 10 120s /bin/sh -c 'echo '\''hello'\'' && printf '\''%s'\'' "done"' >>"$LOG" 2>&1
CODE=$?
printf '%s\t%s\t%s\t%s\n' "$START_ISO" "$(( $(date +%s) - START_S ))" "$CODE" "$TRIGGER" >>"$RUNS"
tail -c 262144 "$LOG" >"$LOG.trim" 2>/dev/null && mv "$LOG.trim" "$LOG"
tail -n 200 "$RUNS" >"$RUNS.trim" 2>/dev/null && mv "$RUNS.trim" "$RUNS"
exit "$CODE"
`

const expectedCron = `# Managed by Nexa Panel. Task Nightly report (0123456789abcdef0123456789abcdef) for site demo.
SHELL=/bin/sh
PATH=/usr/local/bin:/usr/bin:/bin
*/5 1-6 * * 1 nexa_demo /etc/nexa-panel/generated/tasks/nexa-task-0123456789abcdef0123456789abcdef.sh cron
`

func TestRendererProducesExactWrapperAndCronFile(t *testing.T) {
	plan, err := (Renderer{}).Render(testTask(), false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != PlanKind || len(plan.Artifacts) != 2 {
		t.Fatalf("plan shape = %q with %d artifacts", plan.Kind, len(plan.Artifacts))
	}
	wrapper := plan.Artifacts[0]
	if wrapper.Path != "/etc/nexa-panel/generated/tasks/nexa-task-"+testTaskID+".sh" || wrapper.Mode != 0o750 || wrapper.Owner != "root" || wrapper.Group != "nexa_demo" || wrapper.Remove {
		t.Fatalf("wrapper artifact = %+v", wrapper)
	}
	if wrapper.Content != expectedWrapper {
		t.Fatalf("wrapper content mismatch:\n--- got ---\n%s\n--- want ---\n%s", wrapper.Content, expectedWrapper)
	}
	cron := plan.Artifacts[1]
	if cron.Path != "/etc/cron.d/nexa-task-"+testTaskID || cron.Mode != 0o644 || cron.Owner != "root" || cron.Group != "root" || cron.Remove {
		t.Fatalf("cron artifact = %+v", cron)
	}
	if cron.Content != expectedCron {
		t.Fatalf("cron content mismatch:\n--- got ---\n%s\n--- want ---\n%s", cron.Content, expectedCron)
	}
}

func TestRendererDisabledTaskRemovesCronEntry(t *testing.T) {
	task := testTask()
	task.Enabled = false
	plan, err := (Renderer{}).Render(task, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Artifacts[0].Remove || plan.Artifacts[0].Content != expectedWrapper {
		t.Fatalf("disabled wrapper artifact = %+v", plan.Artifacts[0])
	}
	if !plan.Artifacts[1].Remove || plan.Artifacts[1].Content != "" {
		t.Fatalf("disabled cron artifact = %+v", plan.Artifacts[1])
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("disabling a task should warn that its cron entry is removed")
	}
}

func TestRendererRemovalMarksEveryArtifact(t *testing.T) {
	plan, err := (Renderer{}).Render(testTask(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Artifacts) != 2 {
		t.Fatalf("artifact count = %d", len(plan.Artifacts))
	}
	for _, artifact := range plan.Artifacts {
		if !artifact.Remove || artifact.Content != "" {
			t.Fatalf("removal artifact = %+v", artifact)
		}
	}
}

func TestRendererRejectsUntrustedTasks(t *testing.T) {
	for name, mutate := range map[string]func(*Task){
		"short task id":       func(task *Task) { task.ID = "abc123" },
		"uppercase task id":   func(task *Task) { task.ID = strings.ToUpper(testTaskID) },
		"empty name":          func(task *Task) { task.Name = "" },
		"oversized name":      func(task *Task) { task.Name = strings.Repeat("n", 65) },
		"control name":        func(task *Task) { task.Name = "evil\nname" },
		"empty command":       func(task *Task) { task.Command = "" },
		"newline command":     func(task *Task) { task.Command = "echo hi\nrm -rf /" },
		"carriage command":    func(task *Task) { task.Command = "echo hi\rrm -rf /" },
		"nul command":         func(task *Task) { task.Command = "echo\x00hi" },
		"oversized command":   func(task *Task) { task.Command = strings.Repeat("x", 2049) },
		"timeout too small":   func(task *Task) { task.TimeoutSeconds = 9 },
		"timeout too large":   func(task *Task) { task.TimeoutSeconds = 86401 },
		"bad cron":            func(task *Task) { task.CronExpression = "@daily" },
		"foreign root path":   func(task *Task) { task.Scope.RootPath = "/etc" },
		"foreign unix user":   func(task *Task) { task.Scope.UnixUser = "root" },
		"missing site id":     func(task *Task) { task.Scope.SiteID = "" },
		"unmanaged site slug": func(task *Task) { task.Scope.Slug = "Demo" },
	} {
		t.Run(name, func(t *testing.T) {
			task := testTask()
			mutate(&task)
			if _, err := (Renderer{}).Render(task, false); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateCronTable(t *testing.T) {
	valid := []string{
		"* * * * *",
		"*/5 0-23/2 1,15 * 1-5",
		"0 3 * * 0",
		"59 23 31 12 7",
		"0,30 */4 * * *",
		"1-5,10-20/3 * * * *",
		"05 04 * * *",
	}
	for _, expression := range valid {
		if err := ValidateCron(expression); err != nil {
			t.Errorf("ValidateCron(%q) = %v, want nil", expression, err)
		}
	}
	invalid := []string{
		"",
		"* * * *",
		"* * * * * *",
		"@daily",
		"@reboot * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 13 *",
		"* * * 0 *",
		"* * * * 8",
		"5-1 * * * *",
		"5/2 * * * *",
		"*/0 * * * *",
		"*/ * * * *",
		"MON * * * *",
		"* * * * mon",
		"1,,2 * * * *",
		"-5 * * * *",
		"1- * * * *",
		"+5 * * * *",
		"1.5 * * * *",
	}
	for _, expression := range invalid {
		if err := ValidateCron(expression); err == nil {
			t.Errorf("ValidateCron(%q) = nil, want error", expression)
		}
	}
}
