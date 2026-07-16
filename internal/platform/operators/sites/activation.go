package sites

import (
	"net/http"

	"path/filepath"

	"fmt"
	"strings"

	"context"
	"errors"
	"time"
)

type NodeSystem interface {
	PrepareSite(ctx context.Context, site Site) error
	SecureArtifacts(ctx context.Context, site Site, artifacts []Artifact) error
	ValidatePHP(ctx context.Context, version string) error
	ValidateNginx(ctx context.Context) error
	ReloadPHP(ctx context.Context, version string) error
	ReloadNginx(ctx context.Context) error
	VerifyHost(ctx context.Context, site Site) error
}

type HostOperator struct {
	renderer    Renderer
	enabledRoot string
	system      NodeSystem
	now         func() time.Time
}

func NewHostOperator(renderer Renderer, enabledRoot string, system NodeSystem) (*HostOperator, error) {
	if system == nil {
		return nil, errors.New("site node system is required")
	}
	if enabledRoot == "" {
		enabledRoot = "/etc/nginx/sites-enabled"
	}
	if !filepath.IsAbs(enabledRoot) {
		return nil, errors.New("Nginx enabled root must be absolute")
	}
	return &HostOperator{renderer: renderer, enabledRoot: filepath.Clean(enabledRoot), system: system, now: time.Now}, nil
}

func (o *HostOperator) Plan(_ context.Context, site Site) (Plan, error) {
	plan, err := o.renderer.Render(site)
	if err != nil {
		return Plan{}, err
	}
	plan.ID = randomID()
	plan.Kind = PlanKind
	plan.PlannedAt = o.now().UTC()
	plan.ExpiresAt = plan.PlannedAt.Add(30 * time.Minute)
	plan.Before = make([]Snapshot, 0, len(plan.Artifacts))
	filtered := make([]Artifact, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		before, err := readSnapshot(artifact.Path)
		if err != nil {
			return Plan{}, err
		}
		if artifact.Kind == "site-root" && before.Exists {
			continue
		}
		plan.Before = append(plan.Before, before)
		filtered = append(filtered, artifact)
	}
	plan.Artifacts = filtered
	plan.EnabledBefore, err = o.enabled(site.Slug)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (o *HostOperator) Apply(ctx context.Context, plan Plan) (Observation, error) {
	if err := o.validatePlan(plan); err != nil {
		return Observation{}, err
	}
	if o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("site activation plan has expired")
	}
	if err := o.checkBefore(plan); err != nil {
		return Observation{}, err
	}
	if err := o.system.PrepareSite(ctx, plan.Site); err != nil {
		return Observation{}, fmt.Errorf("prepare site identity and directories: %w", err)
	}
	if err := o.writeArtifacts(plan.Artifacts); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, err)
	}
	if err := o.system.SecureArtifacts(ctx, plan.Site, plan.Artifacts); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("secure site artifacts: %w", err))
	}
	if err := o.setEnabled(plan.Site.Slug, true); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, err)
	}
	if err := o.system.ValidatePHP(ctx, plan.Site.PHPVersion); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("validate PHP-FPM configuration: %w", err))
	}
	if err := o.system.ValidateNginx(ctx); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("validate Nginx configuration: %w", err))
	}
	if err := o.system.ReloadPHP(ctx, plan.Site.PHPVersion); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("reload PHP-FPM: %w", err))
	}
	if err := o.system.ReloadNginx(ctx); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("reload Nginx: %w", err))
	}
	if err := o.system.VerifyHost(ctx, plan.Site); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("verify site response: %w", err))
	}
	return o.observe(plan)
}

func (o *HostOperator) rollbackFailure(ctx context.Context, plan Plan, cause error) error {
	failures := []string{cause.Error()}
	if err := o.restore(plan); err != nil {
		failures = append(failures, "restore: "+err.Error())
	}
	if err := o.system.ReloadPHP(context.WithoutCancel(ctx), plan.Site.PHPVersion); err != nil {
		failures = append(failures, "restore PHP-FPM reload: "+err.Error())
	}
	if err := o.system.ReloadNginx(context.WithoutCancel(ctx)); err != nil {
		failures = append(failures, "restore Nginx reload: "+err.Error())
	}
	return errors.New(strings.Join(failures, "; "))
}

func (o *HostOperator) Rollback(ctx context.Context, plan Plan) (Observation, error) {
	if err := o.validatePlan(plan); err != nil {
		return Observation{}, err
	}
	for _, artifact := range plan.Artifacts {
		current, err := readSnapshot(artifact.Path)
		if err != nil {
			return Observation{}, err
		}
		if !current.Exists || current.Digest != digestString(artifact.Content) {
			return Observation{}, errors.New("managed site changed after activation; automatic rollback is unsafe")
		}
	}
	if err := o.restore(plan); err != nil {
		return Observation{}, err
	}
	if err := o.system.ValidateNginx(ctx); err != nil {
		return Observation{}, err
	}
	if err := o.system.ReloadPHP(ctx, plan.Site.PHPVersion); err != nil {
		return Observation{}, err
	}
	if err := o.system.ReloadNginx(ctx); err != nil {
		return Observation{}, err
	}
	return o.observe(plan)
}

type HostSystem struct {
	command func(context.Context, string, ...string) ([]byte, error)
	client  *http.Client
}
