package deploy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

// SiteModeSwitcher persists a site's deployment mode and starts the re-apply
// that makes the node match it. It is the optional half of SiteCatalog — the
// sites module owns the sites table and the site.settings job path, and this
// module drives neither directly — and is asserted from the catalog at call
// time, exactly as the operator narrows its node system to a sub-surface.
//
// A nil job means the mode was stored but nothing had to be re-applied: a site
// that is not active has no live vhost to re-render, and its next plan picks
// the mode up.
type SiteModeSwitcher interface {
	SetDeploymentMode(ctx context.Context, siteID, mode string, actorUserID *string) (*jobs.Job, error)
}

// SharedEnv is the deployer layout's shared environment file as the page shows
// it. Content is in the read shape because editing it is the whole point; it is
// the one value in this module that is a secret in both directions, so it never
// reaches a job payload, an audit entry, or a log line.
type SharedEnv struct {
	SiteID     string     `json:"siteId"`
	Path       string     `json:"path"`
	Present    bool       `json:"present"`
	Content    string     `json:"content"`
	Bytes      int        `json:"bytes"`
	SHA256     string     `json:"sha256"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
}

// ModeChange is what the caller learns from a mode switch: the mode that is now
// stored, and the job re-applying it when one was needed.
type ModeChange struct {
	SiteID string    `json:"siteId"`
	Mode   string    `json:"mode"`
	Job    *jobs.Job `json:"job,omitempty"`
}

// switchMode records the intent and hands the change to the sites module, which
// persists the column and submits the existing site.settings job. The audit
// entry is written first and fail-closed: a switch that cannot be recorded is
// refused, because it re-points a live document root.
func (m *Module) switchMode(ctx context.Context, site sites.Site, mode string, actor *string, remoteAddress string) (ModeChange, error) {
	if mode != sites.DeploymentModeStandard && mode != sites.DeploymentModeDeployer {
		return ModeChange{}, refuse(http.StatusUnprocessableEntity, "invalid_deployment_mode", "The deployment mode must be standard or deployer.")
	}
	// The same settled-state gate the settings editor uses, restated against the
	// exported statuses: a mode switch re-renders the vhost, so it must not
	// interleave with an in-flight plan, activation, rollback, or deletion.
	if !modeSwitchable(site.Status) {
		return ModeChange{}, refuse(http.StatusConflict, "site_busy", "The site is mid-operation; wait for the current job to finish before changing its deployment mode.")
	}
	switcher, err := m.modeSwitcher()
	if err != nil {
		return ModeChange{}, err
	}
	metadata := map[string]any{"mode": mode, "previousMode": site.DeploymentMode}
	if err := m.record(ctx, "deploy.mode_changed", site, actor, remoteAddress, metadata); err != nil {
		return ModeChange{}, err
	}
	// Leaving deployer mode withdraws the site's PHP-FPM reload permission
	// before the column moves. Order matters: a switch that fails after the
	// withdrawal leaves a deployer-mode site without a grant, which the next
	// prepare run restores, whereas a switch that succeeded before a failed
	// withdrawal would leave a standard-mode site holding a sudoers rule.
	if mode == sites.DeploymentModeStandard && site.DeploymentMode == sites.DeploymentModeDeployer {
		if err := m.applyReloadGrant(ctx, reloadPayload(site), false); err != nil {
			return ModeChange{}, refuse(http.StatusConflict, "deploy_operation_failed", err.Error())
		}
	}
	job, err := switcher.SetDeploymentMode(ctx, site.ID, mode, actor)
	if err != nil {
		return ModeChange{}, refuse(http.StatusConflict, "deployment_mode_failed", err.Error())
	}
	return ModeChange{SiteID: site.ID, Mode: mode, Job: job}, nil
}

// TeardownSiteDeployment withdraws the node-side grants this module installed
// for a site that is being removed. It is called by the sites module's delete
// job while the row still exists, and is idempotent: a standard-mode site never
// held a grant, and withdrawing one twice is not an error.
func (m *Module) TeardownSiteDeployment(ctx context.Context, siteID string) error {
	site, err := m.sites.Get(ctx, siteID)
	if err != nil {
		return err
	}
	if site.DeploymentMode != sites.DeploymentModeDeployer {
		return nil
	}
	return m.applyReloadGrant(ctx, reloadPayload(site), false)
}

// reloadPayload names the site to the reload grant. It reuses the prepare
// payload shape so the two callers cannot drift on which identity fields the
// operator re-derives.
func reloadPayload(site sites.Site) prepareJobPayload {
	return prepareJobPayload{
		Slug: site.Slug, UnixUser: site.UnixUser,
		RootPath: site.RootPath, PHPVersion: site.PHPVersion,
	}
}

// sharedEnv reads {root}/app/shared/.env from the node. It is synchronous for
// the reason the whole surface is: a job's request and result are persisted,
// and this document holds a site's credentials.
func (m *Module) sharedEnv(ctx context.Context, site sites.Site) (SharedEnv, error) {
	if err := requireDeployerMode(site); err != nil {
		return SharedEnv{}, err
	}
	document, err := m.operator.ReadSharedEnv(ctx, envRequest(site))
	if err != nil {
		return SharedEnv{}, envRefusal(err)
	}
	return presentSharedEnv(site.ID, document), nil
}

// writeSharedEnv replaces the document. The audit entry carries its size and
// digest and nothing else — never the content, and never a diff of it — and it
// is written before the node is asked, so the intent survives a failed write.
func (m *Module) writeSharedEnv(ctx context.Context, site sites.Site, content string, actor *string, remoteAddress string) (SharedEnv, error) {
	if err := requireDeployerMode(site); err != nil {
		return SharedEnv{}, err
	}
	// Bounded here as well as on the node: the request that typed an oversized
	// document should be told so, not the agent round trip after it.
	if err := deployoperator.ValidateSharedEnv(content); err != nil {
		return SharedEnv{}, refuse(http.StatusUnprocessableEntity, "invalid_shared_env", err.Error())
	}
	metadata := map[string]any{"bytes": len(content), "sha256": deployoperator.SharedEnvDigest(content)}
	if err := m.record(ctx, "deploy.env_updated", site, actor, remoteAddress, metadata); err != nil {
		return SharedEnv{}, err
	}
	document, err := m.operator.WriteSharedEnv(ctx, envRequest(site), content)
	if err != nil {
		return SharedEnv{}, envRefusal(err)
	}
	return presentSharedEnv(site.ID, document), nil
}

// requireDeployerMode refuses the shared environment for a standard site. The
// tree it lives in only exists in deployer mode, and answering "not found"
// would read as a node fault rather than as the mode it is.
func requireDeployerMode(site sites.Site) error {
	if site.DeploymentMode != sites.DeploymentModeDeployer {
		return refuse(http.StatusConflict, "not_deployer_mode", "This site is in standard mode. Switch it to deployer mode to use a shared environment file.")
	}
	return nil
}

// modeSwitcher narrows the catalog to the mode-switching half. The real
// catalog always satisfies it — cmd/nexa/api.go asserts the pairing at compile
// time, so a signature change breaks the build rather than reaching this
// refusal — which leaves this branch for a catalog substituted in a test.
func (m *Module) modeSwitcher() (SiteModeSwitcher, error) {
	switcher, ok := m.sites.(SiteModeSwitcher)
	if !ok {
		return nil, refuse(http.StatusNotImplemented, "deployment_mode_unsupported", "This panel build cannot change a site's deployment mode.")
	}
	return switcher, nil
}

// modeSwitchable mirrors the sites module's settingsEditable predicate. It is
// restated rather than shared because that predicate is unexported and the two
// modules agree on the statuses, not on the function.
func modeSwitchable(status sites.Status) bool {
	switch status {
	case sites.StatusActive, sites.StatusPlanReady, sites.StatusDraft, sites.StatusRolledBack, sites.StatusFailed:
		return true
	default:
		return false
	}
}

func envRequest(site sites.Site) deployoperator.EnvRequest {
	return deployoperator.EnvRequest{Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath}
}

func presentSharedEnv(siteID string, document deployoperator.EnvDocument) SharedEnv {
	shared := SharedEnv{
		SiteID: siteID, Path: document.Path, Present: document.Present,
		Content: document.Content, Bytes: document.Bytes, SHA256: document.SHA256,
	}
	if !document.ModifiedAt.IsZero() {
		modified := document.ModifiedAt
		shared.ModifiedAt = &modified
	}
	return shared
}

// envRefusal names the one node answer the panel can act on: a release tree the
// site does not have yet. Everything else is reported as an operation failure.
func envRefusal(err error) error {
	if errors.Is(err, deployoperator.ErrSharedEnvMissing) {
		return refuse(http.StatusConflict, "shared_env_missing", "The node has no release directory for this site yet. Re-apply the site after switching it to deployer mode.")
	}
	return refuse(http.StatusConflict, "deploy_operation_failed", err.Error())
}
