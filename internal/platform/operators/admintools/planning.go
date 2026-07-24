package admintools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var containerImagePattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)

func (o *HostOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	change, err := normalize(change)
	if err != nil {
		return Plan{}, err
	}
	observed, err := o.Discover(ctx)
	if err != nil {
		return Plan{}, err
	}
	fingerprint, err := fingerprint(observed)
	if err != nil {
		return Plan{}, err
	}
	steps := []string{}
	warnings := []string{}
	switch change.Action {
	case ActionDeploy:
		if change.Tool.Kind == PHPMyAdmin {
			steps = []string{"Install the Ubuntu phpMyAdmin and PHP-FPM packages.", "Write the loopback-only Nginx and root-owned lifecycle configuration.", "Start the native web client and verify it is active."}
			warnings = append(warnings, "phpMyAdmin shares the host Nginx and PHP-FPM runtime; stopping it removes only its private loopback route.")
		} else {
			steps = []string{"Write the root-owned Quadlet definition and idle-stop units.", "Reload systemd and start the localhost-only tool service.", "Verify the container service is active."}
			warnings = append(warnings, fmt.Sprintf("The container may use up to %d MiB and stops 15 minutes after its latest launch; the Podman CLI itself is transient.", change.Tool.MemoryMB))
		}
	case ActionStart:
		steps = []string{"Start the existing tool service.", "Verify the service is active."}
	case ActionRestart:
		steps = []string{"Restart the tool service without touching its configuration or credentials.", "Verify the service is active."}
	case ActionStop:
		steps = []string{"Stop the tool service.", "Verify the service is inactive."}
	case ActionLaunch:
		steps = []string{"Create a short-lived server-side tool session.", "Return session material only to the trusted control plane."}
	}
	now := o.now().UTC()
	return Plan{ID: randomID(), Kind: PlanKind, Change: change, Steps: steps, Warnings: warnings, ObservedFingerprint: fingerprint, PlannedAt: now, ExpiresAt: now.Add(20 * time.Minute)}, nil
}

func normalize(change Change) (Change, error) {
	defaults := Defaults()
	var base *Tool
	for index := range defaults {
		if defaults[index].Kind == change.Tool.Kind {
			copy := defaults[index]
			base = &copy
		}
	}
	if base == nil {
		return Change{}, errors.New("admin tool must be phpmyadmin or pgadmin")
	}
	if change.Action != ActionDeploy && change.Action != ActionStart && change.Action != ActionRestart && change.Action != ActionStop && change.Action != ActionLaunch {
		return Change{}, errors.New("admin tool action is unsupported")
	}
	if change.Tool.Port != 0 {
		base.Port = change.Tool.Port
	}
	if base.Port < 1024 || base.Port > 65535 {
		return Change{}, errors.New("admin tool port is outside the safe range")
	}
	if base.Kind == PHPMyAdmin {
		if change.Tool.Image != "" || change.Tool.ContainerName != "" || change.Tool.MemoryMB != 0 || change.Tool.PIDsLimit != 0 {
			return Change{}, errors.New("native phpMyAdmin does not accept container settings")
		}
	} else {
		if change.Tool.Image != "" {
			base.Image = strings.TrimSpace(change.Tool.Image)
		}
		if change.Tool.MemoryMB != 0 {
			base.MemoryMB = change.Tool.MemoryMB
		}
		if len(base.Image) > 512 || !containerImagePattern.MatchString(base.Image) {
			return Change{}, errors.New("admin tool image reference is invalid")
		}
		if base.MemoryMB < 64 || base.MemoryMB > 1024 {
			return Change{}, errors.New("admin tool memory limit is outside the safe range")
		}
	}
	change.Tool = *base
	if change.Action == ActionLaunch {
		launch := change.Launch
		if launch == nil || !validToken(launch.SessionID) || strings.TrimSpace(launch.PanelUser) == "" || strings.TrimSpace(launch.DatabaseHost) == "" || launch.DatabasePort < 1 || launch.DatabasePort > 65535 || strings.TrimSpace(launch.Database) == "" || strings.TrimSpace(launch.Username) == "" || len(launch.SecretSHA256) != 64 {
			return Change{}, errors.New("complete, credential-bound admin tool launch metadata is required")
		}
	}
	return change, nil
}
