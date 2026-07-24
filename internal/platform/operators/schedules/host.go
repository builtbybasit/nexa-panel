package schedules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

const (
	// runOutputTailBytes bounds how much task output ever crosses the agent
	// boundary after a manual run.
	runOutputTailBytes = 64 * 1024
	// runsFileMaxBytes bounds the runs TSV read; the wrapper trims the file
	// to 200 lines, so anything larger is hostile or corrupt.
	runsFileMaxBytes = 256 * 1024
	// runDeadlineSlack gives the wrapper's own timeout(1) room to fire and
	// record the run before the agent kills the whole invocation.
	runDeadlineSlack = 30 * time.Second
	snapshotMaxBytes = 512 * 1024
)

type HostConfig struct {
	SiteRoot       string
	TaskScriptRoot string
	CronRoot       string
}

type HostOperator struct {
	renderer Renderer
	owner    Ownership
	runner   Runner
	now      func() time.Time
}

var _ Operator = (*HostOperator)(nil)

func NewHostOperator(config HostConfig, owner Ownership, runner Runner) (*HostOperator, error) {
	if owner == nil {
		return nil, errors.New("schedules ownership manager is required")
	}
	if runner == nil {
		runner = execRunner{}
	}
	if config.SiteRoot == "" {
		config.SiteRoot = "/srv/nexa/sites"
	}
	if config.TaskScriptRoot == "" {
		config.TaskScriptRoot = "/etc/nexa-panel/generated/tasks"
	}
	if config.CronRoot == "" {
		config.CronRoot = "/etc/cron.d"
	}
	if !filepath.IsAbs(config.SiteRoot) || !filepath.IsAbs(config.TaskScriptRoot) || !filepath.IsAbs(config.CronRoot) {
		return nil, errors.New("schedules site, task script, and cron roots must be absolute")
	}
	renderer := Renderer{SiteRoot: filepath.Clean(config.SiteRoot), TaskScriptRoot: filepath.Clean(config.TaskScriptRoot), CronRoot: filepath.Clean(config.CronRoot)}
	return &HostOperator{renderer: renderer, owner: owner, runner: runner, now: time.Now}, nil
}

func (o *HostOperator) Plan(_ context.Context, task Task, removal bool) (Plan, error) {
	plan, err := o.renderer.Render(task, removal)
	if err != nil {
		return Plan{}, err
	}
	plan.ID = randomID()
	plan.PlannedAt = o.now().UTC()
	plan.ExpiresAt = plan.PlannedAt.Add(PlanTTL)
	plan.Before = make([]Snapshot, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		before, err := readSnapshot(artifact.Path)
		if err != nil {
			return Plan{}, err
		}
		plan.Before = append(plan.Before, before)
	}
	return plan, nil
}

// validatePlan re-renders the plan's task and rejects any artifact the
// renderer would not produce, so a compromised control plane cannot use the
// agent to write arbitrary files.
func (o *HostOperator) validatePlan(plan Plan) error {
	if plan.ID == "" || plan.Kind != PlanKind || len(plan.Artifacts) == 0 || len(plan.Artifacts) != len(plan.Before) {
		return invalid("The schedule plan is invalid.")
	}
	removal := true
	for _, artifact := range plan.Artifacts {
		if !artifact.Remove {
			removal = false
		}
	}
	expected, err := o.renderer.Render(plan.Task, removal)
	if err != nil {
		return err
	}
	if len(expected.Artifacts) != len(plan.Artifacts) {
		return invalid("The schedule plan contains an unmanaged artifact.")
	}
	for index, artifact := range plan.Artifacts {
		exact := expected.Artifacts[index]
		if exact != artifact || plan.Before[index].Path != artifact.Path {
			return invalid("The schedule plan contains an unmanaged artifact.")
		}
	}
	return nil
}

func (o *HostOperator) checkBefore(plan Plan) error {
	for _, expected := range plan.Before {
		current, err := readSnapshot(expected.Path)
		if err != nil {
			return err
		}
		if current.Exists != expected.Exists || current.Digest != expected.Digest {
			return conflict("The scheduled task changed on this node after planning; create a new plan.")
		}
	}
	return nil
}

func (o *HostOperator) Apply(_ context.Context, plan Plan) (Observation, error) {
	if err := o.validatePlan(plan); err != nil {
		return Observation{}, err
	}
	if o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, conflict("The schedule plan has expired; create a new plan.")
	}
	if err := o.checkBefore(plan); err != nil {
		return Observation{}, err
	}
	removal := true
	for _, artifact := range plan.Artifacts {
		if !artifact.Remove {
			removal = false
		}
	}
	if !removal {
		if err := o.ensureTaskLogDirectory(plan.Task.Scope); err != nil {
			return Observation{}, err
		}
	}
	if err := o.writeArtifacts(plan.Artifacts); err != nil {
		return Observation{}, o.restoreFailure(plan, err)
	}
	return o.observe(plan)
}

func (o *HostOperator) Rollback(_ context.Context, plan Plan) (Observation, error) {
	if err := o.validatePlan(plan); err != nil {
		return Observation{}, err
	}
	for _, artifact := range plan.Artifacts {
		current, err := readSnapshot(artifact.Path)
		if err != nil {
			return Observation{}, err
		}
		if artifact.Remove {
			if current.Exists {
				return Observation{}, conflict("The scheduled task changed after this plan was applied; automatic rollback is unsafe.")
			}
			continue
		}
		if !current.Exists || current.Digest != digestString(artifact.Content) {
			return Observation{}, conflict("The scheduled task changed after this plan was applied; automatic rollback is unsafe.")
		}
	}
	if err := o.restore(plan); err != nil {
		return Observation{}, err
	}
	return o.observe(plan)
}

func (o *HostOperator) Run(ctx context.Context, task Task) (RunResult, error) {
	if err := validateTask(task, o.renderer.siteRoot()); err != nil {
		return RunResult{}, err
	}
	wrapper := o.renderer.wrapperPath(task)
	if info, err := os.Lstat(wrapper); err != nil || !info.Mode().IsRegular() {
		return RunResult{}, notFound("The scheduled task is not installed on this node; apply its plan first.")
	}
	deadline := time.Duration(task.TimeoutSeconds)*time.Second + runDeadlineSlack
	runCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	startedAt := o.now().UTC()
	exitCode, output, err := o.runner.Run(runCtx, Command{Name: "runuser", Args: []string{"-u", task.Scope.UnixUser, "--", wrapper, "manual"}})
	durationMs := o.now().UTC().Sub(startedAt).Milliseconds()
	if err != nil {
		return RunResult{}, conflict(commandFailure("execute scheduled task", output, err))
	}
	result := RunResult{ExitCode: exitCode, DurationMs: durationMs, StartedAt: startedAt, TimedOut: exitCode == 124 || exitCode == 137}
	tail, err := o.readOutputTail(task)
	if err != nil {
		return RunResult{}, err
	}
	result.OutputTail = tail
	return result, nil
}

func (o *HostOperator) Runs(_ context.Context, scope sitefs.Scope, taskID string) ([]RunRecord, error) {
	if !taskIDPattern.MatchString(taskID) {
		return nil, invalid("The task identifier is not a managed task identifier.")
	}
	if err := sitefs.Validate(scope, o.renderer.siteRoot()); err != nil {
		return nil, invalid("The site scope is invalid: " + err.Error() + ".")
	}
	root, err := sitefs.OpenRoot(scope)
	if err != nil {
		return nil, notFound("The site directory is not present on this node.")
	}
	defer root.Close()
	file, err := root.Open("logs/tasks/" + taskID + ".runs")
	if errors.Is(err, fs.ErrNotExist) {
		return []RunRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, runsFileMaxBytes))
	if err != nil {
		return nil, err
	}
	records := parseRunRecords(string(data))
	if len(records) > RunsMaxRecords {
		records = records[len(records)-RunsMaxRecords:]
	}
	return records, nil
}

// parseRunRecords reads the wrapper's TSV rows: started-at, duration in
// seconds, exit code, trigger. Overlap skips write "skipped-overlap" in the
// exit-code column and surface as ExitCode -1. Malformed rows are dropped.
func parseRunRecords(data string) []RunRecord {
	records := []RunRecord{}
	for _, line := range strings.Split(data, "\n") {
		columns := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(columns) != 4 {
			continue
		}
		startedAt, err := time.Parse(time.RFC3339, columns[0])
		if err != nil {
			continue
		}
		record := RunRecord{StartedAt: startedAt.UTC(), Trigger: columns[3]}
		if record.Trigger == "" {
			continue
		}
		if columns[2] == "skipped-overlap" {
			record.ExitCode = -1
			records = append(records, record)
			continue
		}
		duration, err := strconv.Atoi(columns[1])
		if err != nil || duration < 0 {
			continue
		}
		exitCode, err := strconv.Atoi(columns[2])
		if err != nil {
			continue
		}
		record.DurationSeconds = duration
		record.ExitCode = exitCode
		records = append(records, record)
	}
	return records
}

func (o *HostOperator) readOutputTail(task Task) (string, error) {
	root, err := sitefs.OpenRoot(task.Scope)
	if err != nil {
		return "", notFound("The site directory is not present on this node.")
	}
	defer root.Close()
	file, err := root.Open("logs/tasks/" + task.ID + ".log")
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", nil
	}
	start := info.Size() - runOutputTailBytes
	if start < 0 {
		start = 0
	}
	data := make([]byte, info.Size()-start)
	if _, err := io.ReadFull(io.NewSectionReader(file, start, int64(len(data))), data); err != nil {
		return "", err
	}
	return string(data), nil
}

// ensureTaskLogDirectory creates <root>/logs/tasks (0770, owned by the site
// account) through the confined root so the wrapper and manual runs always
// have somewhere to log.
func (o *HostOperator) ensureTaskLogDirectory(scope sitefs.Scope) error {
	root, err := sitefs.OpenRoot(scope)
	if err != nil {
		return notFound("The site directory is not present on this node.")
	}
	defer root.Close()
	for _, directory := range []string{"logs", "logs/tasks"} {
		if err := root.Mkdir(directory, 0o770); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if err := root.Chmod("logs/tasks", 0o770); err != nil {
		return err
	}
	return o.owner.Chown(filepath.Join(scope.RootPath, "logs", "tasks"), scope.UnixUser, "www-data")
}

func (o *HostOperator) writeArtifacts(artifacts []Artifact) error {
	for _, artifact := range artifacts {
		if artifact.Remove {
			if err := os.Remove(artifact.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := atomicWrite(artifact.Path, []byte(artifact.Content), os.FileMode(artifact.Mode)); err != nil {
			return err
		}
		if err := o.owner.Chown(artifact.Path, artifact.Owner, artifact.Group); err != nil {
			return err
		}
	}
	return nil
}

func (o *HostOperator) restoreFailure(plan Plan, cause error) error {
	failures := []string{cause.Error()}
	if err := o.restore(plan); err != nil {
		failures = append(failures, "restore: "+err.Error())
	}
	return conflict(strings.Join(failures, "; "))
}

func (o *HostOperator) restore(plan Plan) error {
	var failures []string
	for index := len(plan.Before) - 1; index >= 0; index-- {
		if err := writeSnapshot(plan.Before[index]); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (o *HostOperator) observe(plan Plan) (Observation, error) {
	items := make([]Snapshot, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		snapshot, err := readSnapshot(artifact.Path)
		if err != nil {
			return Observation{}, err
		}
		items = append(items, snapshot)
	}
	return Observation{TaskID: plan.Task.ID, Artifacts: items, VerifiedAt: o.now().UTC()}, nil
}

func readSnapshot(path string) (Snapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{Path: path}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > snapshotMaxBytes {
		return Snapshot{}, conflict("A managed schedule artifact is unsafe or too large.")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Path: path, Exists: true, Mode: uint32(info.Mode().Perm()), Digest: digestBytes(content), Content: string(content)}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		snapshot.UID = int(stat.Uid)
		snapshot.GID = int(stat.Gid)
	}
	return snapshot, nil
}

func writeSnapshot(snapshot Snapshot) error {
	if !snapshot.Exists {
		if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := atomicWrite(snapshot.Path, []byte(snapshot.Content), os.FileMode(snapshot.Mode)); err != nil {
		return err
	}
	return os.Chown(snapshot.Path, snapshot.UID, snapshot.GID)
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".nexa-stage-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func digestBytes(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func digestString(content string) string { return digestBytes([]byte(content)) }

func randomID() string { return secureid.Hex(16) }

func commandFailure(action string, output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		return fmt.Sprintf("%s: %v", action, err)
	}
	return fmt.Sprintf("%s: %v: %s", action, err, message)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command Command) (int, []byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	buffer := &hostcmd.Writer{Limit: 64 * 1024}
	process.Stdout = buffer
	process.Stderr = buffer
	err := process.Run()
	if err == nil {
		return 0, buffer.Bytes(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), buffer.Bytes(), nil
	}
	return -1, buffer.Bytes(), err
}
