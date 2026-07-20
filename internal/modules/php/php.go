// Package php is the control-plane feature module behind the PHP page. It
// surfaces, per installed PHP branch, the loadable extension (module) catalog
// and the php.ini directive set, and orchestrates extension install/remove and
// directive saves as durable jobs through the privileged PHP operator. Each
// mutation is planned and applied back-to-back inside one job: the agent signs
// the plan it just issued and the module hands it straight back to apply, so the
// FastPanel-style one-click / single-save UX still travels the signed path.
package php

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	phpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/php"
)

// SiteCatalog resolves a site by id; the sites module provides the concrete
// implementation. AccessPolicy scopes which sites a user may reach — both mirror
// how the files and logs modules depend on sites.
type SiteCatalog interface {
	Get(ctx context.Context, id string) (sites.Site, error)
}

type AccessPolicy interface {
	SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error)
}

type Module struct {
	jobs     *jobs.Module
	operator phpoperator.Operator
	sites    SiteCatalog
	access   AccessPolicy
}

func New(queue *jobs.Module, operator phpoperator.Operator, catalog SiteCatalog, access AccessPolicy) (*Module, error) {
	if queue == nil || operator == nil || catalog == nil || access == nil {
		return nil, errors.New("php jobs, operator, site catalog, and access policy are required")
	}
	m := &Module{jobs: queue, operator: operator, sites: catalog, access: access}
	if err := queue.RegisterHandler("php.apply", m.applyJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "php", Name: "PHP", Version: "0.1.0",
		Description:          "Manage PHP extensions and php.ini settings per installed PHP version, plus per-site overrides.",
		Dependencies:         []string{"identity", "jobs", "sites"},
		RequiredCapabilities: []string{"apt"},
		EstimatedIdleBytes:   256 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error { return m.registerHTTP(registry) }

// Versions lists the PHP branches installed on the node.
func (m *Module) Versions(ctx context.Context) ([]string, error) {
	return m.operator.Versions(ctx)
}

// Extensions lists the loadable modules a branch offers, joined with install
// state.
func (m *Module) Extensions(ctx context.Context, version string) ([]phpoperator.Extension, error) {
	return m.operator.Extensions(ctx, version)
}

// Settings lists a branch's php.ini directives as the FPM SAPI sees them.
func (m *Module) Settings(ctx context.Context, version string) ([]phpoperator.Directive, error) {
	return m.operator.Settings(ctx, version)
}

// ChangeExtension queues an install or remove of one module for a branch.
func (m *Module) ChangeExtension(ctx context.Context, version, extension string, action phpoperator.Action, actor *string) (jobs.Job, error) {
	version = strings.TrimSpace(version)
	extension = strings.TrimSpace(extension)
	if version == "" || extension == "" {
		return jobs.Job{}, errors.New("a PHP version and extension are required")
	}
	verb := "Install"
	if action == phpoperator.ActionRemoveExtension {
		verb = "Remove"
	} else if action != phpoperator.ActionInstallExtension {
		return jobs.Job{}, errors.New("extension action must be install or remove")
	}
	change := phpoperator.Change{Action: action, Version: version, Extension: extension}
	title := verb + " php" + version + "-" + extension
	return m.jobs.SubmitTitled(ctx, "php.apply", title, change, actor)
}

// SaveSettings queues a directive save for a branch: Set writes values, Reset
// reverts directives to their PHP default.
func (m *Module) SaveSettings(ctx context.Context, version string, set map[string]string, reset []string, actor *string) (jobs.Job, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return jobs.Job{}, errors.New("a PHP version is required")
	}
	if len(set) == 0 && len(reset) == 0 {
		return jobs.Job{}, errors.New("no php.ini changes were requested")
	}
	change := phpoperator.Change{Action: phpoperator.ActionSaveSettings, Version: version, Set: set, Reset: reset}
	return m.jobs.SubmitTitled(ctx, "php.apply", "Save PHP "+version+" settings", change, actor)
}

// siteScope projects a resolved site into the operator's per-site scope. The
// operator re-validates every field against the sites root, so this is a
// convenience, not the trust boundary.
func siteScope(site sites.Site) phpoperator.SiteScope {
	return phpoperator.SiteScope{Slug: site.Slug, Version: site.PHPVersion, RootPath: site.RootPath, UnixUser: site.UnixUser}
}

// SiteSettings lists the directives a site may override, each with its
// branch-wide value overlaid by any per-site override.
func (m *Module) SiteSettings(ctx context.Context, site sites.Site) ([]phpoperator.Directive, error) {
	return m.operator.SiteSettings(ctx, siteScope(site))
}

// SaveSiteSettings queues a per-site override save into the site's .user.ini.
func (m *Module) SaveSiteSettings(ctx context.Context, site sites.Site, set map[string]string, reset []string, actor *string) (jobs.Job, error) {
	if len(set) == 0 && len(reset) == 0 {
		return jobs.Job{}, errors.New("no per-site php.ini changes were requested")
	}
	scope := siteScope(site)
	change := phpoperator.Change{Action: phpoperator.ActionSaveSiteSettings, Version: site.PHPVersion, Site: &scope, Set: set, Reset: reset}
	return m.jobs.SubmitTitled(ctx, "php.apply", "Save PHP settings for "+site.Slug, change, actor)
}

// applyJob plans and applies one PHP change. The operator validates the change
// against the node's real state on both the plan and apply calls; the agent
// signs the plan in between, so an unsigned or drifted change never applies.
func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var change phpoperator.Change
	if err := json.Unmarshal(raw, &change); err != nil || change.Action == "" || change.Version == "" {
		return nil, errors.New("invalid PHP change request")
	}
	_ = report(20, "Planning the PHP change.")
	plan, err := m.operator.Plan(ctx, change)
	if err != nil {
		return nil, err
	}
	_ = report(60, "Applying the signed PHP plan.")
	observation, err := m.operator.Apply(ctx, plan)
	if err != nil {
		return nil, err
	}
	_ = report(95, "PHP change is verified.")
	return observation, nil
}
