package schedules

import (
	"path/filepath"

	"strconv"
	"strings"
)

// Renderer turns a validated task into its two managed artifacts: the
// root-owned wrapper script and the /etc/cron.d entry. It is pure so the
// agent can re-render a submitted plan and reject unmanaged artifacts.
type Renderer struct {
	SiteRoot       string
	TaskScriptRoot string
	CronRoot       string
}

func (r Renderer) siteRoot() string {
	if r.SiteRoot == "" {
		return "/srv/nexa/sites"
	}
	return r.SiteRoot
}

func (r Renderer) taskScriptRoot() string {
	if r.TaskScriptRoot == "" {
		return "/etc/nexa-panel/generated/tasks"
	}
	return r.TaskScriptRoot
}

func (r Renderer) cronRoot() string {
	if r.CronRoot == "" {
		return "/etc/cron.d"
	}
	return r.CronRoot
}

func (r Renderer) wrapperPath(task Task) string {
	return filepath.Join(r.taskScriptRoot(), "nexa-task-"+task.ID+".sh")
}

func (r Renderer) cronPath(task Task) string {
	return filepath.Join(r.cronRoot(), "nexa-task-"+task.ID)
}

// Render validates the task and produces the plan's artifacts. A removal
// plan marks every artifact Remove; a disabled task keeps its wrapper but
// marks the cron entry Remove so applying the plan uninstalls the schedule.
func (r Renderer) Render(task Task, removal bool) (Plan, error) {
	if err := validateTask(task, r.siteRoot()); err != nil {
		return Plan{}, err
	}
	wrapper := Artifact{Path: r.wrapperPath(task), Mode: 0o750, Owner: "root", Group: task.Scope.UnixUser}
	cron := Artifact{Path: r.cronPath(task), Mode: 0o644, Owner: "root", Group: "root"}
	warnings := []string{}
	switch {
	case removal:
		wrapper.Remove = true
		cron.Remove = true
		warnings = append(warnings, "Applying this plan removes the task's wrapper script and cron entry; its logs are kept under the site's logs/tasks directory.")
	case task.Enabled:
		wrapper.Content = r.renderWrapper(task)
		cron.Content = r.renderCron(task)
	default:
		wrapper.Content = r.renderWrapper(task)
		cron.Remove = true
		warnings = append(warnings, "The task is disabled: applying this plan removes its cron entry until it is enabled again.")
	}
	return Plan{Kind: PlanKind, Task: task, Artifacts: []Artifact{wrapper, cron}, Warnings: warnings}, nil
}

func (r Renderer) renderWrapper(task Task) string {
	escaped := strings.ReplaceAll(task.Command, "'", `'\''`)
	replacer := strings.NewReplacer(
		"<name>", task.Name,
		"<id>", task.ID,
		"<slug>", task.Scope.Slug,
		"<unixuser>", task.Scope.UnixUser,
		"<root>", task.Scope.RootPath,
		"<timeout>", strconv.Itoa(task.TimeoutSeconds),
		"<escaped command>", escaped,
	)
	return replacer.Replace(wrapperTemplate)
}

func (r Renderer) renderCron(task Task) string {
	return "# Managed by Nexa Panel. Task " + task.Name + " (" + task.ID + ") for site " + task.Scope.Slug + ".\n" +
		"SHELL=/bin/sh\n" +
		"PATH=/usr/local/bin:/usr/bin:/bin\n" +
		task.CronExpression + " " + task.Scope.UnixUser + " " + r.wrapperPath(task) + " cron\n"
}

// wrapperTemplate is the exact POSIX sh wrapper contract: flock-based overlap
// skipping, a hard timeout, bounded log and run-record files, and a TSV run
// record per invocation. The command is embedded single-quoted with each
// single quote escaped (quote, backslash-quote, quote) so it can never break
// out of the sh -c argument.
const wrapperTemplate = `#!/bin/sh
# Managed by Nexa Panel. Task <name> (<id>) for site <slug>. Runs as <unixuser>.
set -u
TRIGGER="${1:-manual}"
ROOT='<root>'
LOG="$ROOT/logs/tasks/<id>.log"
RUNS="$ROOT/logs/tasks/<id>.runs"
mkdir -p "$ROOT/logs/tasks" || exit 111
exec 9>>"$ROOT/tmp/.nexa-task-<id>.lock" || exit 111
if ! flock -n 9; then
  printf '%s\t0\tskipped-overlap\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$TRIGGER" >>"$RUNS"
  exit 0
fi
START_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; START_S="$(date +%s)"
cd "$ROOT" || exit 111
timeout -k 10 <timeout>s /bin/sh -c '<escaped command>' >>"$LOG" 2>&1
CODE=$?
printf '%s\t%s\t%s\t%s\n' "$START_ISO" "$(( $(date +%s) - START_S ))" "$CODE" "$TRIGGER" >>"$RUNS"
tail -c 262144 "$LOG" >"$LOG.trim" 2>/dev/null && mv "$LOG.trim" "$LOG"
tail -n 200 "$RUNS" >"$RUNS.trim" 2>/dev/null && mv "$RUNS.trim" "$RUNS"
exit "$CODE"
`
