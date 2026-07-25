package schedules

import (
	"context"
	"net/http"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

const PlanKind = "nexa.schedule.change.v1"

// PlanTTL bounds how long a rendered schedule plan stays applicable; the
// before-state drift check makes a stale plan unsafe to hold longer.
const PlanTTL = 15 * time.Minute

// RunsMaxRecords bounds how many run records ever cross the agent boundary;
// the wrapper script trims the runs file to the same bound.
const RunsMaxRecords = 200

// Task is one scheduled command for a managed site. It is sent by the
// control panel and independently re-validated by the agent before any
// artifact is rendered or executed.
type Task struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	CronExpression string       `json:"cronExpression"`
	Command        string       `json:"command"`
	TimeoutSeconds int          `json:"timeoutSeconds"`
	Enabled        bool         `json:"enabled"`
	Scope          sitefs.Scope `json:"scope"`
}

type Artifact struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Content string `json:"content"`
	Owner   string `json:"owner"`
	Group   string `json:"group"`
	Remove  bool   `json:"remove"`
}

type Plan struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Task      Task       `json:"task"`
	Artifacts []Artifact `json:"artifacts"`
	Before    []Snapshot `json:"before"`
	Warnings  []string   `json:"warnings"`
	PlannedAt time.Time  `json:"plannedAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	Signature string     `json:"signature,omitempty"`
}

type Snapshot struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Mode    uint32 `json:"mode,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Content string `json:"content,omitempty"`
	UID     int    `json:"uid,omitempty"`
	GID     int    `json:"gid,omitempty"`
}

type Observation struct {
	TaskID     string     `json:"taskId"`
	Artifacts  []Snapshot `json:"artifacts"`
	VerifiedAt time.Time  `json:"verifiedAt"`
}

type RunResult struct {
	ExitCode   int       `json:"exitCode"`
	DurationMs int64     `json:"durationMs"`
	StartedAt  time.Time `json:"startedAt"`
	OutputTail string    `json:"outputTail"`
	TimedOut   bool      `json:"timedOut"`
}

type RunRecord struct {
	StartedAt       time.Time `json:"startedAt"`
	DurationSeconds int       `json:"durationSeconds"`
	ExitCode        int       `json:"exitCode"`
	Trigger         string    `json:"trigger"`
}

type Operator interface {
	Plan(ctx context.Context, task Task, removal bool) (Plan, error)
	Apply(ctx context.Context, plan Plan) (Observation, error)
	Rollback(ctx context.Context, plan Plan) (Observation, error)
	Run(ctx context.Context, task Task) (RunResult, error)
	Runs(ctx context.Context, scope sitefs.Scope, taskID string) ([]RunRecord, error)
}

type Command struct {
	Name string
	Args []string
}

// Runner executes one host command and reports its exit code. A non-nil
// error means the command could not be executed at all; a failing command
// returns its exit code without an error.
type Runner interface {
	Run(ctx context.Context, command Command) (int, []byte, error)
}

// Ownership assigns rendered artifacts to their owning accounts. The wrapper
// script is root-owned but group-readable by the site account, so the
// interface carries an explicit group.
type Ownership interface {
	Chown(path, owner, group string) error
}

type HostOwnership struct{}

func (HostOwnership) Chown(path, owner, group string) error {
	account, err := user.Lookup(owner)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if group != "" && group != owner {
		resolved, err := user.LookupGroup(group)
		if err != nil {
			return err
		}
		if gid, err = strconv.Atoi(resolved.Gid); err != nil {
			return err
		}
	}
	return os.Chown(path, uid, gid)
}

// Error codes cross the agent boundary so handlers on both sides map the
// same failure to the same HTTP status and envelope code.
const (
	CodeInvalid  = "schedule_invalid"
	CodeNotFound = "schedule_not_found"
	CodeConflict = "schedule_conflict"
)

type OperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *OperationError) Error() string { return e.Message }

func StatusFor(code string) int {
	switch code {
	case CodeInvalid:
		return http.StatusUnprocessableEntity
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func invalid(message string) *OperationError {
	return &OperationError{Code: CodeInvalid, Message: message}
}

func notFound(message string) *OperationError {
	return &OperationError{Code: CodeNotFound, Message: message}
}

func conflict(message string) *OperationError {
	return &OperationError{Code: CodeConflict, Message: message}
}

var taskIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Field bounds are shared with the control-panel module so both sides reject
// the same tasks; the agent still re-validates because plans and run requests
// arrive over the socket.
const (
	NameMaxLength     = 64
	CommandMaxLength  = 2048
	TimeoutMinSeconds = 10
	TimeoutMaxSeconds = 86400
)

func ValidateName(name string) error {
	if name == "" || len([]rune(name)) > NameMaxLength {
		return invalid("The task name must contain 1-64 characters.")
	}
	for _, value := range name {
		if !unicode.IsPrint(value) {
			return invalid("The task name may only contain printable characters.")
		}
	}
	return nil
}

func ValidateCommand(command string) error {
	if command == "" || len(command) > CommandMaxLength {
		return invalid("The task command must contain 1-2048 characters.")
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return invalid("The task command must be a single line without control characters.")
	}
	return nil
}

func ValidateTimeout(seconds int) error {
	if seconds < TimeoutMinSeconds || seconds > TimeoutMaxSeconds {
		return invalid("The task timeout must be between 10 and 86400 seconds.")
	}
	return nil
}

var cronBounds = [5]struct {
	name string
	min  int
	max  int
}{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day of month", 1, 31},
	{"month", 1, 12},
	{"day of week", 0, 7},
}

// ValidateCron accepts exactly five whitespace-separated fields, each a comma
// list of `*`, `N`, or `N-M`, with an optional `/step` on `*` and ranges.
// Names such as @daily or MON are rejected: rendered cron lines stay numeric.
func ValidateCron(expression string) error {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return invalid("The cron expression must contain exactly five fields (minute, hour, day of month, month, day of week).")
	}
	for index, field := range fields {
		bounds := cronBounds[index]
		for _, item := range strings.Split(field, ",") {
			if err := validateCronItem(item, bounds.min, bounds.max); err != nil {
				return invalid("The cron " + bounds.name + " field is invalid: " + err.Error() + ".")
			}
		}
	}
	return nil
}

func validateCronItem(item string, min, max int) error {
	value, step, hasStep := strings.Cut(item, "/")
	if hasStep {
		parsed, ok := cronNumber(step)
		if !ok || parsed < 1 || parsed > max {
			return &OperationError{Code: CodeInvalid, Message: "a step must be a number between 1 and " + strconv.Itoa(max)}
		}
	}
	if value == "*" {
		return nil
	}
	low, high, isRange := strings.Cut(value, "-")
	from, err := parseCronNumber(low, min, max)
	if err != nil {
		return err
	}
	if !isRange {
		if hasStep {
			return &OperationError{Code: CodeInvalid, Message: "steps are only allowed on * and ranges"}
		}
		return nil
	}
	to, err := parseCronNumber(high, min, max)
	if err != nil {
		return err
	}
	if from > to {
		return &OperationError{Code: CodeInvalid, Message: "a range must run from low to high"}
	}
	return nil
}

func validateTask(task Task, siteRoot string) error {
	if !taskIDPattern.MatchString(task.ID) {
		return invalid("The task identifier is not a managed task identifier.")
	}
	if err := sitefs.Validate(task.Scope, siteRoot); err != nil {
		return invalid("The site scope is invalid: " + err.Error() + ".")
	}
	if err := ValidateName(task.Name); err != nil {
		return err
	}
	if err := ValidateCommand(task.Command); err != nil {
		return err
	}
	if err := ValidateTimeout(task.TimeoutSeconds); err != nil {
		return err
	}
	return ValidateCron(task.CronExpression)
}

func parseCronNumber(value string, min, max int) (int, error) {
	parsed, ok := cronNumber(value)
	if !ok {
		return -1, &OperationError{Code: CodeInvalid, Message: "values must be plain numbers (names like @daily or MON are not supported)"}
	}
	if parsed < min || parsed > max {
		return -1, &OperationError{Code: CodeInvalid, Message: "values must be between " + strconv.Itoa(min) + " and " + strconv.Itoa(max)}
	}
	return parsed, nil
}

// cronNumber accepts unsigned decimal digits only: signs, spaces, names, and
// empty strings all fail.
func cronNumber(value string) (int, bool) {
	if value == "" {
		return -1, false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return -1, false
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1, false
	}
	return parsed, true
}
