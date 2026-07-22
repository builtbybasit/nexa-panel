package sites

import (
	"context"
	"testing"
)

func TestCreateDefaultsToStandardDeploymentMode(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")

	if site.DeploymentMode != DeploymentModeStandard {
		t.Fatalf("created site mode = %q, want %q", site.DeploymentMode, DeploymentModeStandard)
	}
	loaded, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeploymentMode != DeploymentModeStandard {
		t.Fatalf("read-back mode = %q, want %q", loaded.DeploymentMode, DeploymentModeStandard)
	}
}

func TestDefinitionThreadsDeploymentMode(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")

	def, err := module.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if def.DeploymentMode != DeploymentModeStandard {
		t.Fatalf("definition mode = %q, want %q", def.DeploymentMode, DeploymentModeStandard)
	}

	setDeploymentMode(t, module, site.ID, DeploymentModeDeployer)
	def, err = module.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if def.DeploymentMode != DeploymentModeDeployer {
		t.Fatalf("definition mode = %q, want %q", def.DeploymentMode, DeploymentModeDeployer)
	}
}

// A row whose column predates the migration — or that a downgrade left with a
// value the renderer does not know — must read back as standard rather than
// pushing an unknown mode at the operator.
func TestUnknownStoredDeploymentModeReadsBackAsStandard(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")

	for _, stored := range []string{"", "capistrano"} {
		setDeploymentMode(t, module, site.ID, stored)
		loaded, err := module.Get(ctx, site.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.DeploymentMode != DeploymentModeStandard {
			t.Fatalf("stored %q read back as %q, want %q", stored, loaded.DeploymentMode, DeploymentModeStandard)
		}
		def, err := module.Definition(ctx, site.ID, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if def.DeploymentMode != DeploymentModeStandard {
			t.Fatalf("stored %q reached the operator as %q", stored, def.DeploymentMode)
		}
	}
}

func TestValidDeploymentMode(t *testing.T) {
	for value, want := range map[string]bool{
		"standard": true, "deployer": true, "": false, "Standard": false, "deploy": false,
	} {
		if got := validDeploymentMode(value); got != want {
			t.Fatalf("validDeploymentMode(%q) = %v, want %v", value, got, want)
		}
	}
}

func setDeploymentMode(t *testing.T, m *Module, siteID, mode string) {
	t.Helper()
	if _, err := m.database.NewUpdate().Model((*siteModel)(nil)).Set("deployment_mode = ?", mode).Where("id = ?", siteID).Exec(context.Background()); err != nil {
		t.Fatalf("set deployment mode: %v", err)
	}
}

// SetDeploymentMode is the exported half of the settings follow-up path: it
// persists the column and, for a live site, queues the re-render that repoints
// the document root at the release tree.
func TestSetDeploymentModePersistsAndReAppliesForALiveSite(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")
	setStatus(t, module, site.ID, StatusActive)

	job, err := module.SetDeploymentMode(ctx, site.ID, DeploymentModeDeployer, nil)
	if err != nil {
		t.Fatalf("SetDeploymentMode returned an error: %v", err)
	}
	if job == nil || job.Kind != "site.settings" {
		t.Fatalf("job = %+v, want a site.settings job", job)
	}
	loaded, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeploymentMode != DeploymentModeDeployer {
		t.Fatalf("stored mode = %q, want %q", loaded.DeploymentMode, DeploymentModeDeployer)
	}
	if loaded.LastJobID == nil || *loaded.LastJobID != job.ID {
		t.Fatalf("the re-apply job was not attached to the site: %+v", loaded.LastJobID)
	}
	waitForJob(t, queue, job.ID)
}

// A plan_ready site re-plans instead, so the stored plan cannot go stale behind
// the mode it was rendered for.
func TestSetDeploymentModeRePlansAPlanReadySite(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")
	setStatus(t, module, site.ID, StatusPlanReady)

	job, err := module.SetDeploymentMode(ctx, site.ID, DeploymentModeDeployer, nil)
	if err != nil {
		t.Fatalf("SetDeploymentMode returned an error: %v", err)
	}
	if job == nil || job.Kind != "site.plan" {
		t.Fatalf("job = %+v, want a site.plan job", job)
	}
	waitForJob(t, queue, job.ID)
}

// A settled site that is not live has no vhost to re-render, so the column moves
// and no job is queued; the next plan reads it.
func TestSetDeploymentModeStoresWithoutAJobForASettledSite(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")
	setStatus(t, module, site.ID, StatusDraft)

	job, err := module.SetDeploymentMode(ctx, site.ID, DeploymentModeDeployer, nil)
	if err != nil {
		t.Fatalf("SetDeploymentMode returned an error: %v", err)
	}
	if job != nil {
		t.Fatalf("job = %+v, want none", job)
	}
	loaded, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeploymentMode != DeploymentModeDeployer {
		t.Fatalf("stored mode = %q, want %q", loaded.DeploymentMode, DeploymentModeDeployer)
	}
}

// The gate is restated inside the exported method rather than trusted from the
// caller: this repoints a live document root.
func TestSetDeploymentModeRefusesUnknownModesAndBusySites(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")
	setStatus(t, module, site.ID, StatusActive)

	for _, mode := range []string{"", "Deployer", "release", "standard;rm -rf /"} {
		if _, err := module.SetDeploymentMode(ctx, site.ID, mode, nil); err == nil {
			t.Fatalf("SetDeploymentMode accepted %q", mode)
		}
	}
	setStatus(t, module, site.ID, StatusActivating)
	if _, err := module.SetDeploymentMode(ctx, site.ID, DeploymentModeDeployer, nil); err == nil {
		t.Fatal("SetDeploymentMode accepted a change to a mid-operation site")
	}
	loaded, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeploymentMode != DeploymentModeStandard {
		t.Fatalf("a refused change was stored anyway: %q", loaded.DeploymentMode)
	}
}
