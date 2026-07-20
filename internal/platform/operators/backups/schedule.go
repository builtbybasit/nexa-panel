package backups

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// systemdRoot is where per-plan timer/service units are written. The agent unit
// must list this in ReadWritePaths (ProtectSystem=strict blocks it otherwise).
const systemdRoot = "/etc/systemd/system"

// hostSystemdRoot returns the operator's unit directory, defaulting to the real
// one when unset (kept overridable so tests can write into a temp dir).
func (h *HostOperator) hostSystemdRoot() string {
	if h.systemdRoot == "" {
		return systemdRoot
	}
	return h.systemdRoot
}

// ScheduleSpec is everything the agent needs to install a plan's timer.
type ScheduleSpec struct {
	PlanID      string `json:"planId"`
	PlanName    string `json:"planName"`
	Cron        string `json:"cron"`
	StateDBPath string `json:"stateDbPath"`
}

func unitBase(planID string) string { return "nexa-backup-" + planID }

var (
	safePlanIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	safeUnitPathPattern = regexp.MustCompile(`^/[A-Za-z0-9_./:@+\-]+$`)
)

// InstallSchedule writes (or overwrites) a plan's .service + .timer, reloads
// systemd, and enables the timer. Overwriting makes it idempotent: editing a
// plan's schedule just rewrites the units.
func (h *HostOperator) InstallSchedule(ctx context.Context, spec ScheduleSpec) error {
	onCalendar, err := validateScheduleSpec(spec, h.nexaBinary)
	if err != nil {
		return err
	}
	base := unitBase(spec.PlanID)
	if err := writeUnitAtomically(filepath.Join(h.hostSystemdRoot(), base+".service"), []byte(h.renderService(spec))); err != nil {
		return fmt.Errorf("write backup service unit: %w", err)
	}
	if err := writeUnitAtomically(filepath.Join(h.hostSystemdRoot(), base+".timer"), []byte(renderTimer(spec, onCalendar))); err != nil {
		return fmt.Errorf("write backup timer unit: %w", err)
	}
	if _, err := h.runner.Run(ctx, "systemctl", []string{"daemon-reload"}, nil); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if _, err := h.runner.Run(ctx, "systemctl", []string{"enable", "--now", base + ".timer"}, nil); err != nil {
		return fmt.Errorf("enable backup timer: %w", err)
	}
	return nil
}

// RemoveSchedule is idempotent, but reports real disable/delete/reload failures
// so the control plane can retain and reconcile the desired state.
func (h *HostOperator) RemoveSchedule(ctx context.Context, planID string) error {
	if !safePlanIDPattern.MatchString(planID) {
		return errors.New("backup plan ID contains unsafe characters")
	}
	base := unitBase(planID)
	timerPath := filepath.Join(h.hostSystemdRoot(), base+".timer")
	servicePath := filepath.Join(h.hostSystemdRoot(), base+".service")
	_, timerErr := os.Stat(timerPath)
	_, serviceErr := os.Stat(servicePath)
	if errors.Is(timerErr, os.ErrNotExist) && errors.Is(serviceErr, os.ErrNotExist) {
		return nil
	}
	if timerErr != nil && !errors.Is(timerErr, os.ErrNotExist) {
		return fmt.Errorf("inspect backup timer unit: %w", timerErr)
	}
	if serviceErr != nil && !errors.Is(serviceErr, os.ErrNotExist) {
		return fmt.Errorf("inspect backup service unit: %w", serviceErr)
	}
	if !errors.Is(timerErr, os.ErrNotExist) {
		if _, err := h.runner.Run(ctx, "systemctl", []string{"disable", "--now", base + ".timer"}, nil); err != nil {
			return fmt.Errorf("disable backup timer: %w", err)
		}
	}
	for _, path := range []string{timerPath, servicePath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove backup schedule unit: %w", err)
		}
	}
	if _, err := h.runner.Run(ctx, "systemctl", []string{"daemon-reload"}, nil); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	return nil
}

func (h *HostOperator) renderService(spec ScheduleSpec) string {
	return "[Unit]\n" +
		"Description=Nexa Panel backup plan " + unitDescription(spec.PlanName) + " (" + spec.PlanID + ")\n" +
		"After=nexa-api.service\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"User=nexa\n" +
		"Group=nexa\n" +
		"ExecStart=" + h.nexaBinary + " backup trigger --plan " + spec.PlanID + " --state " + spec.StateDBPath + "\n"
}

func renderTimer(spec ScheduleSpec, onCalendar string) string {
	return "[Unit]\n" +
		"Description=Schedule for Nexa Panel backup plan " + unitDescription(spec.PlanName) + " (" + spec.PlanID + ")\n\n" +
		"[Timer]\n" +
		"OnCalendar=" + onCalendar + "\n" +
		"Persistent=true\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
}

func validateScheduleSpec(spec ScheduleSpec, binary string) (string, error) {
	if !safePlanIDPattern.MatchString(spec.PlanID) {
		return "", errors.New("backup plan ID contains unsafe characters")
	}
	name := strings.TrimSpace(spec.PlanName)
	if name == "" || len(name) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("backup plan name must contain 1-80 printable characters")
	}
	for label, path := range map[string]string{"state database": spec.StateDBPath, "nexa binary": binary} {
		if !safeUnitPathPattern.MatchString(path) || filepath.Clean(path) != path {
			return "", fmt.Errorf("%s path is not safe for a systemd unit", label)
		}
	}
	return ValidateSchedule(spec.Cron)
}

func unitDescription(value string) string {
	// Percent introduces systemd specifiers even in descriptions.
	return strings.ReplaceAll(strings.TrimSpace(value), "%", "%%")
}

func writeUnitAtomically(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".nexa-backup-unit-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// ValidateSchedule validates and converts a 5-field cron expression (minute hour day-of-month
// month day-of-week) to a systemd OnCalendar expression. It handles the token
// vocabulary the schedule editor emits: `*`, single values, comma lists, ranges
// (`a-b`), and steps (`*/n`, `a-b/n`). Day-of-week numbers become systemd
// weekday names; a `*` day-of-week is omitted entirely.
func ValidateSchedule(expression string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return "", fmt.Errorf("schedule must be a 5-field cron expression, got %q", expression)
	}
	definitions := []struct {
		name    string
		minimum int
		maximum int
		steps   bool
	}{
		{name: "minute", minimum: 0, maximum: 59, steps: true},
		{name: "hour", minimum: 0, maximum: 23, steps: true},
		{name: "day of month", minimum: 1, maximum: 31, steps: true},
		{name: "month", minimum: 1, maximum: 12, steps: true},
		{name: "day of week", minimum: 0, maximum: 7, steps: false},
	}
	for index, definition := range definitions {
		if err := validateCronField(fields[index], definition.minimum, definition.maximum, definition.steps); err != nil {
			return "", fmt.Errorf("invalid %s field: %w", definition.name, err)
		}
	}
	if fields[2] != "*" && fields[4] != "*" {
		return "", errors.New("day-of-month and day-of-week cannot both be restricted")
	}
	minute := convertNumericField(fields[0], 0)
	hour := convertNumericField(fields[1], 0)
	dayOfMonth := convertNumericField(fields[2], 1)
	month := convertNumericField(fields[3], 1)

	date := "*-" + month + "-" + dayOfMonth
	clock := hour + ":" + minute + ":00"
	if fields[4] == "*" {
		return date + " " + clock, nil
	}
	weekdays, err := convertWeekdayField(fields[4])
	if err != nil {
		return "", err
	}
	return weekdays + " " + date + " " + clock, nil
}

func validateCronField(field string, minimum, maximum int, allowSteps bool) error {
	if field == "" {
		return errors.New("field is empty")
	}
	for _, item := range strings.Split(field, ",") {
		if item == "" {
			return errors.New("list contains an empty item")
		}
		base := item
		if strings.Count(item, "/") > 1 {
			return errors.New("step contains too many separators")
		}
		if before, after, found := strings.Cut(item, "/"); found {
			if !allowSteps {
				return errors.New("steps are not supported")
			}
			step, err := strconv.Atoi(after)
			if err != nil || step < 1 || step > maximum-minimum+1 {
				return errors.New("step is outside the allowed range")
			}
			base = before
		}
		if base == "*" {
			continue
		}
		if strings.Count(base, "-") > 1 {
			return errors.New("range contains too many separators")
		}
		if lowToken, highToken, found := strings.Cut(base, "-"); found {
			low, lowErr := parseCronNumber(lowToken, minimum, maximum)
			high, highErr := parseCronNumber(highToken, minimum, maximum)
			if lowErr != nil || highErr != nil || low > high {
				return errors.New("range is invalid or reversed")
			}
			continue
		}
		if _, err := parseCronNumber(base, minimum, maximum); err != nil {
			return err
		}
	}
	return nil
}

func parseCronNumber(token string, minimum, maximum int) (int, error) {
	value, err := strconv.Atoi(token)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("value %q must be between %d and %d", token, minimum, maximum)
	}
	return value, nil
}

// convertNumericField rewrites cron step/range syntax into systemd's: `*/n`
// becomes `0/n` (systemd has no bare-wildcard step) and `a-b` becomes `a..b`.
func convertNumericField(field string, minimum int) string {
	if field == "*" {
		return "*"
	}
	parts := strings.Split(field, ",")
	for index, part := range parts {
		if strings.HasPrefix(part, "*/") {
			parts[index] = strconv.Itoa(minimum) + "/" + part[2:]
			continue
		}
		parts[index] = strings.ReplaceAll(part, "-", "..")
	}
	return strings.Join(parts, ",")
}

var weekdayNames = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

func convertWeekdayField(field string) (string, error) {
	parts := strings.Split(field, ",")
	converted := make([]string, 0, len(parts))
	for _, part := range parts {
		if bounds := strings.SplitN(part, "-", 2); len(bounds) == 2 {
			low, err := weekdayName(bounds[0])
			if err != nil {
				return "", err
			}
			high, err := weekdayName(bounds[1])
			if err != nil {
				return "", err
			}
			converted = append(converted, low+".."+high)
			continue
		}
		name, err := weekdayName(part)
		if err != nil {
			return "", err
		}
		converted = append(converted, name)
	}
	return strings.Join(converted, ","), nil
}

// weekdayName maps a cron day-of-week number (0-7, where 0 and 7 are Sunday) to
// its systemd weekday name.
func weekdayName(token string) (string, error) {
	value, err := strconv.Atoi(token)
	if err != nil || value < 0 || value > 7 {
		return "", fmt.Errorf("invalid day-of-week %q", token)
	}
	return weekdayNames[value%7], nil
}
