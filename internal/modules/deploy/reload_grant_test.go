package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

func runPrepareJobFor(t *testing.T, module *Module, payload prepareJobPayload) NodePreparation {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := module.prepareJob(context.Background(), raw, func(int, string) error { return nil })
	if err != nil {
		t.Fatalf("prepareJob returned an error: %v", err)
	}
	preparation, ok := result.(NodePreparation)
	if !ok {
		t.Fatalf("result = %T, want NodePreparation", result)
	}
	return preparation
}

func deployerPayload() prepareJobPayload {
	return prepareJobPayload{
		SiteID: testSite.ID, PHPVersion: "8.3", Slug: testSite.Slug,
		UnixUser: testSite.UnixUser, RootPath: testSite.RootPath, DeployerMode: true,
	}
}

// The narrow reload permission exists for deployer-mode sites and no others: a
// standard-mode site's document root is the panel's, so nothing it runs ever
// needs to reload PHP-FPM.
func TestPrepareGrantsTheReloadPermissionOnlyInDeployerMode(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	module.sites.(*fakeCatalog).site = deployerSite

	standard := deployerPayload()
	standard.DeployerMode = false
	runPrepareJobFor(t, module, standard)
	if len(operator.reloadGrants) != 0 {
		t.Fatalf("a standard-mode prepare asked the node for %+v", operator.reloadGrants)
	}

	runPrepareJobFor(t, module, deployerPayload())
	if len(operator.reloadGrants) != 1 {
		t.Fatalf("reload grants = %d, want 1", len(operator.reloadGrants))
	}
	granted := operator.reloadGrants[0]
	want := deployoperator.FPMReloadRequest{
		Slug: testSite.Slug, UnixUser: testSite.UnixUser,
		RootPath: testSite.RootPath, PHPVersion: "8.3", Enabled: true,
	}
	if granted != want {
		t.Fatalf("granted = %+v, want %+v", granted, want)
	}
}

// A grant that could not be installed must not read as a prepared node — the
// deployer would publish a release and then be unable to serve it — but it also
// must not fail the run, whose whole purpose is to report what is missing.
func TestAFailedReloadGrantWarnsAndClearsReady(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	operator.reloadErr = errors.New("visudo rejected the rule")
	module.sites.(*fakeCatalog).site = deployerSite

	preparation := runPrepareJobFor(t, module, deployerPayload())
	if preparation.Ready {
		t.Fatal("a node that refused the reload grant reported ready")
	}
	if !containsSubstring(preparation.Warnings, "visudo rejected the rule") {
		t.Fatalf("warnings = %q", preparation.Warnings)
	}
}

// Leaving deployer mode withdraws the grant. The sudoers drop-in is the one
// artifact of this feature that outlives the vhost, so nothing may switch a
// site back to standard while it still holds one.
func TestLeavingDeployerModeWithdrawsTheReloadGrant(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	catalog := module.sites.(*fakeCatalog)
	catalog.site = deployerSite

	if _, err := module.switchMode(context.Background(), deployerSite, sites.DeploymentModeStandard, nil, "203.0.113.9"); err != nil {
		t.Fatalf("switchMode returned an error: %v", err)
	}
	if len(operator.reloadGrants) != 1 || operator.reloadGrants[0].Enabled {
		t.Fatalf("reload grants = %+v, want one withdrawal", operator.reloadGrants)
	}
	if len(catalog.modes) != 1 || catalog.modes[0] != sites.DeploymentModeStandard {
		t.Fatalf("stored modes = %v", catalog.modes)
	}
}

// Entering deployer mode withdraws nothing: the grant is installed by the
// prepare run, which is a separate, explicitly requested step.
func TestEnteringDeployerModeTouchesNoGrant(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	if _, err := module.switchMode(context.Background(), standardSite(), sites.DeploymentModeDeployer, nil, "203.0.113.9"); err != nil {
		t.Fatalf("switchMode returned an error: %v", err)
	}
	if len(operator.reloadGrants) != 0 {
		t.Fatalf("entering deployer mode asked the node for %+v", operator.reloadGrants)
	}
}

// A withdrawal the node refused must leave the site in deployer mode. The
// alternative — a standard-mode site still holding a sudoers rule — is the one
// outcome this ordering exists to prevent.
func TestAFailedWithdrawalLeavesTheModeUnchanged(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	catalog := module.sites.(*fakeCatalog)
	catalog.site = deployerSite
	operator.reloadErr = errors.New("node unreachable")

	if _, err := module.switchMode(context.Background(), deployerSite, sites.DeploymentModeStandard, nil, "203.0.113.9"); err == nil {
		t.Fatal("switchMode accepted a mode change whose withdrawal failed")
	}
	if len(catalog.modes) != 0 {
		t.Fatalf("the mode was stored anyway: %v", catalog.modes)
	}
}

// Deleting a site withdraws its grant while the row still exists. A rule naming
// a slug that no longer exists would otherwise be inherited by the next site
// created with that slug.
func TestTeardownWithdrawsTheReloadGrant(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	catalog := module.sites.(*fakeCatalog)

	if err := module.TeardownSiteDeployment(context.Background(), testSite.ID); err != nil {
		t.Fatalf("TeardownSiteDeployment returned an error: %v", err)
	}
	if len(operator.reloadGrants) != 0 {
		t.Fatalf("tearing down a standard-mode site asked the node for %+v", operator.reloadGrants)
	}

	catalog.site = deployerSite
	if err := module.TeardownSiteDeployment(context.Background(), testSite.ID); err != nil {
		t.Fatalf("TeardownSiteDeployment returned an error: %v", err)
	}
	if len(operator.reloadGrants) != 1 || operator.reloadGrants[0].Enabled {
		t.Fatalf("reload grants = %+v, want one withdrawal", operator.reloadGrants)
	}
	if operator.reloadGrants[0].Slug != testSite.Slug {
		t.Fatalf("withdrew the grant for %q", operator.reloadGrants[0].Slug)
	}
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

// A prepare run can spend minutes in apt, and nothing serialises it against a
// mode switch. The grant must therefore follow the site's mode as it reads when
// the grant is written, not the mode captured in the durable payload at submit
// time: switchMode revokes the grant *before* it moves the column, so a stale
// payload re-granting afterwards would strand a NOPASSWD sudoers rule on a
// standard-mode site — and teardown, which early-returns for a non-deployer
// site, would never remove it.
func TestAConcurrentSwitchToStandardWithholdsTheReloadGrant(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	// The site was in deployer mode when the job was submitted; by the time the
	// job reaches the grant, the operator has switched it back to standard.
	module.sites.(*fakeCatalog).site = standardSite()

	preparation := runPrepareJobFor(t, module, deployerPayload())
	if len(operator.reloadGrants) != 0 {
		t.Fatalf("a site switched to standard mid-run was granted %+v", operator.reloadGrants)
	}
	if !preparation.Ready {
		t.Fatalf("withholding the grant failed the run: %q", preparation.Warnings)
	}
}

// The grant is named from the site as re-read, not from the payload, so a
// prepare cannot install a rule for identity fields that no longer describe the
// site.
func TestTheReloadGrantIsNamedFromTheSiteNotThePayload(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	module.sites.(*fakeCatalog).site = deployerSite

	payload := deployerPayload()
	payload.Slug, payload.UnixUser, payload.RootPath = "blog", "nexa_other", "/srv/nexa/sites/other"
	runPrepareJobFor(t, module, payload)
	if len(operator.reloadGrants) != 1 {
		t.Fatalf("reload grants = %d, want 1", len(operator.reloadGrants))
	}
	if granted := operator.reloadGrants[0]; granted.UnixUser != deployerSite.UnixUser || granted.RootPath != deployerSite.RootPath {
		t.Fatalf("granted = %+v, want the site's own identity", granted)
	}
}
