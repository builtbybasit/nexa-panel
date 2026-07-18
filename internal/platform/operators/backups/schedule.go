package backups

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// InstallSchedule writes (or overwrites) a plan's .service + .timer, reloads
// systemd, and enables the timer. Overwriting makes it idempotent: editing a
// plan's schedule just rewrites the units.
func (h *HostOperator) InstallSchedule(ctx context.Context, spec ScheduleSpec) error {
	onCalendar, err := cronToOnCalendar(spec.Cron)
	if err != nil {
		return err
	}
	base := unitBase(spec.PlanID)
	if err := os.WriteFile(filepath.Join(h.hostSystemdRoot(), base+".service"), []byte(h.renderService(spec)), 0o644); err != nil {
		return fmt.Errorf("write backup service unit: %w", err)
	}
	if err := os.WriteFile(filepath.Join(h.hostSystemdRoot(), base+".timer"), []byte(renderTimer(spec, onCalendar)), 0o644); err != nil {
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

// RemoveSchedule disables and deletes a plan's units. It is best-effort per
// step so a partially-installed schedule still cleans up: a missing unit is not
// an error worth failing a plan deletion over.
func (h *HostOperator) RemoveSchedule(ctx context.Context, planID string) error {
	base := unitBase(planID)
	_, _ = h.runner.Run(ctx, "systemctl", []string{"disable", "--now", base + ".timer"}, nil)
	_ = os.Remove(filepath.Join(h.hostSystemdRoot(), base+".timer"))
	_ = os.Remove(filepath.Join(h.hostSystemdRoot(), base+".service"))
	_, _ = h.runner.Run(ctx, "systemctl", []string{"daemon-reload"}, nil)
	return nil
}

func (h *HostOperator) renderService(spec ScheduleSpec) string {
	return "[Unit]\n" +
		"Description=Nexa Panel backup plan " + spec.PlanName + " (" + spec.PlanID + ")\n" +
		"After=nexa-api.service\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"User=nexa\n" +
		"Group=nexa\n" +
		"ExecStart=" + h.nexaBinary + " backup trigger --plan " + spec.PlanID + " --state " + spec.StateDBPath + "\n"
}

func renderTimer(spec ScheduleSpec, onCalendar string) string {
	return "[Unit]\n" +
		"Description=Schedule for Nexa Panel backup plan " + spec.PlanName + " (" + spec.PlanID + ")\n\n" +
		"[Timer]\n" +
		"OnCalendar=" + onCalendar + "\n" +
		"Persistent=true\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
}

// cronToOnCalendar converts a 5-field cron expression (minute hour day-of-month
// month day-of-week) to a systemd OnCalendar expression. It handles the token
// vocabulary the schedule editor emits: `*`, single values, comma lists, ranges
// (`a-b`), and steps (`*/n`, `a-b/n`). Day-of-week numbers become systemd
// weekday names; a `*` day-of-week is omitted entirely.
func cronToOnCalendar(expression string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return "", fmt.Errorf("schedule must be a 5-field cron expression, got %q", expression)
	}
	minute := convertNumericField(fields[0])
	hour := convertNumericField(fields[1])
	dayOfMonth := convertNumericField(fields[2])
	month := convertNumericField(fields[3])

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

// convertNumericField rewrites cron step/range syntax into systemd's: `*/n`
// becomes `0/n` (systemd has no bare-wildcard step) and `a-b` becomes `a..b`.
func convertNumericField(field string) string {
	if field == "*" {
		return "*"
	}
	parts := strings.Split(field, ",")
	for index, part := range parts {
		if strings.HasPrefix(part, "*/") {
			parts[index] = "0/" + part[2:]
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
