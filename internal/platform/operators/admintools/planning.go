package admintools

import (
	"context"
	"strings"
	"time"

	"errors"
	"fmt"
)

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
		steps = []string{"Write the root-owned Quadlet definition.", "Reload systemd and start the localhost-only tool service.", "Verify the container service is active."}
		warnings = append(warnings, fmt.Sprintf("The container may use up to %d MiB; the Podman CLI itself is transient.", change.Tool.MemoryMB))
	case ActionStart:
		steps = []string{"Start the existing tool service.", "Verify the service is active."}
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
	if change.Action != ActionDeploy && change.Action != ActionStart && change.Action != ActionStop && change.Action != ActionLaunch {
		return Change{}, errors.New("admin tool action is unsupported")
	}
	if change.Tool.Image != "" {
		base.Image = strings.TrimSpace(change.Tool.Image)
	}
	if change.Tool.Port != 0 {
		base.Port = change.Tool.Port
	}
	if change.Tool.MemoryMB != 0 {
		base.MemoryMB = change.Tool.MemoryMB
	}
	if base.Port < 1024 || base.Port > 65535 || base.MemoryMB < 64 || base.MemoryMB > 1024 {
		return Change{}, errors.New("admin tool port or memory limit is outside the safe range")
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
