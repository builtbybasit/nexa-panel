package sites

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type NodeSystem interface {
	PrepareSite(ctx context.Context, site Site) error
	SecureArtifacts(ctx context.Context, site Site, artifacts []Artifact) error
	VerifyDocumentRoot(ctx context.Context, site Site) error
	ValidatePHP(ctx context.Context, version string) error
	ValidateNginx(ctx context.Context) error
	ReloadPHP(ctx context.Context, version string) error
	ReloadNginx(ctx context.Context) error
	VerifyHost(ctx context.Context, site Site) error
	// RemoveSite is PrepareSite's counterpart: it deletes the account and the
	// directory tree PrepareSite created, and nothing else.
	RemoveSite(ctx context.Context, site Site) error
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
	for label, root := range map[string]string{
		"Nginx available":   renderer.NginxAvailableRoot,
		"PHP configuration": renderer.PHPConfigRoot,
		"site":              renderer.SiteRoot,
		"socket":            renderer.SocketRoot,
	} {
		if root != "" && !filepath.IsAbs(root) {
			return nil, fmt.Errorf("%s root must be absolute", label)
		}
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
	// Snapshot the retired paths too, so restore() can put back a stanza that a
	// failed activation had already removed.
	plan.RetiredBefore = make([]Snapshot, 0, len(plan.Retired))
	for _, path := range plan.Retired {
		before, err := readSnapshot(path)
		if err != nil {
			return Plan{}, err
		}
		plan.RetiredBefore = append(plan.RetiredBefore, before)
	}
	plan.EnabledBefore, err = o.enabled(site.Slug)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// PlanTeardown renders the site exactly as Plan does — validatePlan re-renders
// and demands byte-exact equality, so the artifacts must still be the real ones —
// but reports an empty before-state for every managed path. Rolling this plan
// back therefore removes each artifact and leaves the vhost disabled. The
// retired paths are blanked the same way, so a conditional artifact is removed
// whether or not its setting happened to be on when the site was torn down.
func (o *HostOperator) PlanTeardown(ctx context.Context, site Site) (Plan, error) {
	plan, err := o.Plan(ctx, site)
	if err != nil {
		return Plan{}, err
	}
	plan.Before = make([]Snapshot, len(plan.Artifacts))
	for index := range plan.Artifacts {
		plan.Before[index] = Snapshot{Path: plan.Artifacts[index].Path}
	}
	plan.RetiredBefore = make([]Snapshot, len(plan.Retired))
	for index, path := range plan.Retired {
		plan.RetiredBefore[index] = Snapshot{Path: path}
	}
	plan.EnabledBefore = false
	plan.Teardown = true
	return plan, nil
}

// Purge removes the site's managed Unix account and its managed site root, the
// two pieces of host state no artifact covers. It runs after the teardown
// rollback has stripped the configuration, and is verified rather than trusted:
// the site is re-validated against this renderer, so only the account and root
// derived from a well-formed slug can ever be named.
func (o *HostOperator) Purge(ctx context.Context, site Site) error {
	if err := o.renderer.validate(site); err != nil {
		return err
	}
	return o.system.RemoveSite(ctx, site)
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
	if err := o.removeRetired(plan); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, err)
	}
	if err := o.system.SecureArtifacts(ctx, plan.Site, plan.Artifacts); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("secure site artifacts: %w", err))
	}
	if err := o.setEnabled(plan.Site.Slug, true); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, err)
	}
	if err := o.system.VerifyDocumentRoot(ctx, plan.Site); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("verify site document root: %w", err))
	}
	if err := o.system.ValidatePHP(ctx, plan.Site.PHPVersion); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("validate PHP-FPM configuration: %w", err))
	}
	if err := o.system.ValidateNginx(ctx); err != nil {
		return Observation{}, o.rollbackFailure(ctx, plan, fmt.Errorf("validate Nginx configuration: %w", err))
	}
	// On a runtime change the outgoing version's FPM master still holds the old
	// pool (and the site's socket). Reload it first so it lets go before the new
	// version binds — best-effort, because a version change is often made
	// precisely because the old runtime is about to be (or already was)
	// uninstalled, and a missing service must not fail the site it no longer
	// serves. The socket is safe either way: FPM unlinks a stale socket path
	// before binding it.
	if old := retiredPHP(plan); old != "" {
		_ = o.system.ReloadPHP(ctx, old)
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
	// The restore put the outgoing version's pool file back; its FPM must
	// reload after the new version's (which just dropped the pool) so the old
	// runtime re-binds the socket and keeps serving. This one is not
	// best-effort: the site's previous configuration depends on it.
	if old := retiredPHP(plan); old != "" {
		if err := o.system.ReloadPHP(context.WithoutCancel(ctx), old); err != nil {
			failures = append(failures, "restore previous PHP-FPM reload: "+err.Error())
		}
	}
	if err := o.system.ReloadNginx(context.WithoutCancel(ctx)); err != nil {
		failures = append(failures, "restore Nginx reload: "+err.Error())
	}
	return errors.New(strings.Join(failures, "; "))
}

// retiredPHP names the runtime whose pool this plan removes, "" when none. The
// self-equality guard mirrors the renderer's, so reload choreography and the
// retired artifact list can never disagree about whether a change happened.
func retiredPHP(plan Plan) string {
	if old := plan.Site.RetiredPHPVersion; old != "" && old != plan.Site.PHPVersion {
		return old
	}
	return ""
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
		// A teardown converges: an artifact it wants gone that is already gone is
		// the end state it is driving towards, not drift. This is what lets a
		// retried or resumed teardown finish instead of refusing forever once the
		// first attempt has removed anything. An artifact that is still present
		// gets the ordinary rollback's digest gate either way, so drifted content
		// is never blindly deleted.
		if !current.Exists {
			if plan.Teardown {
				continue
			}
			return Observation{}, errors.New("managed site changed after activation; automatic rollback is unsafe")
		}
		if current.Digest != digestString(artifact.Content) {
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
	// Same ordering as rollbackFailure: the restored old-version pool must be
	// picked up after the new version released the socket.
	if old := retiredPHP(plan); old != "" {
		if err := o.system.ReloadPHP(ctx, old); err != nil {
			return Observation{}, err
		}
	}
	if err := o.system.ReloadNginx(ctx); err != nil {
		return Observation{}, err
	}
	return o.observe(plan)
}
