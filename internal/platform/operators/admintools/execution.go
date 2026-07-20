package admintools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (o *HostOperator) Apply(ctx context.Context, execution Execution) (Observation, error) {
	plan := execution.Plan
	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("admin tool plan is invalid or expired")
	}
	change, err := normalize(plan.Change)
	if err != nil {
		return Observation{}, err
	}
	observed, err := o.Discover(ctx)
	if err != nil {
		return Observation{}, err
	}
	current, _ := fingerprint(observed)
	if current != plan.ObservedFingerprint {
		return Observation{}, errors.New("admin tool observed state changed after planning")
	}
	if change.Action == ActionLaunch {
		digest := sha256.Sum256([]byte(execution.Secret))
		if change.Launch == nil || !strings.EqualFold(hex.EncodeToString(digest[:]), change.Launch.SecretSHA256) {
			return Observation{}, errors.New("credential does not match the approved admin tool launch")
		}
		return o.bootstrapLaunch(ctx, change, execution.Secret)
	}
	if execution.Secret != "" {
		return Observation{}, errors.New("unexpected secret for admin tool operation")
	}
	if change.Action == ActionDeploy {
		if change.Tool.Kind == PHPMyAdmin {
			if err := o.deployNativePHPMyAdmin(ctx, change.Tool); err != nil {
				return Observation{}, err
			}
		} else {
			if err := os.MkdirAll(o.quadletRoot, 0o755); err != nil {
				return Observation{}, fmt.Errorf("create Quadlet directory: %w", err)
			}
			if err := os.MkdirAll(filepath.Join(o.configRoot, string(change.Tool.Kind)), 0o750); err != nil {
				return Observation{}, fmt.Errorf("create admin tool configuration directory: %w", err)
			}
			if err := o.prepareToolConfig(change.Tool.Kind); err != nil {
				return Observation{}, err
			}
			path := o.quadletPath(change.Tool.Kind)
			if err := secureWrite(path, []byte(renderQuadlet(change.Tool, o.configRoot)), 0o640); err != nil {
				return Observation{}, fmt.Errorf("write admin tool Quadlet: %w", err)
			}
			if err := o.writePGAdminIdleUnits(); err != nil {
				return Observation{}, err
			}
		}
		if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
			return Observation{}, commandError("reload admin tool units", output, err)
		}
	}
	verb := "start"
	if change.Action == ActionDeploy {
		verb = "restart"
	}
	if change.Action == ActionStop {
		verb = "stop"
		if change.Tool.Kind == PGAdmin {
			if err := o.stopPGAdminTimer(ctx); err != nil {
				return Observation{}, err
			}
		}
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{verb, change.Tool.SystemdUnit}}); err != nil {
		// A deploy writes the Quadlet .container file and reloads systemd; the
		// Podman Quadlet generator turns it into the .service unit during that
		// reload. If the unit is still missing here, the generator refused the
		// file — usually an unsupported key for this podman version (the generator
		// drops the whole unit on one bad key). Point at the diagnostic rather
		// than systemctl's opaque "Unit not found".
		if change.Action == ActionDeploy && change.Tool.Kind == PGAdmin && mentionsMissingUnit(output) {
			return Observation{}, fmt.Errorf("%s was not generated from its Quadlet definition — the Podman Quadlet generator rejected it (run `/usr/libexec/podman/quadlet -dryrun` to see which key); ensure Podman with Quadlet support is installed: %w", change.Tool.SystemdUnit, err)
		}
		return Observation{}, commandError(verb+" admin tool", output, err)
	}
	if change.Action == ActionStop && change.Tool.Kind == PGAdmin {
		if err := o.scrubPGAdminLaunchState(); err != nil {
			return Observation{}, err
		}
	}
	output, verifyErr := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", change.Tool.SystemdUnit}})
	status := strings.TrimSpace(string(output))
	if change.Action == ActionStop {
		if status == "active" {
			return Observation{}, errors.New("admin tool remained active after stop")
		}
		change.Tool.Status = "stopped"
	} else {
		if verifyErr != nil || status != "active" {
			return Observation{}, commandError("verify admin tool", output, firstError(verifyErr, errors.New("service is not active")))
		}
		change.Tool.Status = "active"
		if change.Tool.Kind == PGAdmin {
			if err := o.schedulePGAdminStop(ctx); err != nil {
				return Observation{}, err
			}
		}
	}
	return Observation{Tool: change.Tool, Verified: true}, nil
}
