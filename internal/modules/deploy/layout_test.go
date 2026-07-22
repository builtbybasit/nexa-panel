package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

// deployerSite is testSite already switched over, which is the state every
// shared-environment call requires.
var deployerSite = func() sites.Site {
	site := testSite
	site.Status = sites.StatusActive
	site.DeploymentMode = sites.DeploymentModeDeployer
	return site
}()

func standardSite() sites.Site {
	site := testSite
	site.Status = sites.StatusActive
	site.DeploymentMode = sites.DeploymentModeStandard
	return site
}

// The switch hands the change to the sites module, which owns the column and
// the re-apply; this module records who asked for it and reports the job.
func TestSwitchModeDelegatesToTheSitesModuleAndAuditsTheChange(t *testing.T) {
	module, _, _, auditLog, _ := newDeployModule(t)
	catalog := module.sites.(*fakeCatalog)
	catalog.site = standardSite()
	catalog.job = &jobs.Job{ID: 42}
	actor := "user_admin"
	change, err := module.switchMode(context.Background(), catalog.site, sites.DeploymentModeDeployer, &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("switchMode() = %v, want nil", err)
	}
	if change.Mode != sites.DeploymentModeDeployer || change.Job == nil || change.Job.ID != 42 {
		t.Fatalf("change = %+v", change)
	}
	if len(catalog.modes) != 1 || catalog.modes[0] != sites.DeploymentModeDeployer {
		t.Fatalf("modes = %v", catalog.modes)
	}
	recorded := findEvent(t, auditLog, "deploy.mode_changed")
	if recorded.Metadata["mode"] != sites.DeploymentModeDeployer || recorded.Metadata["previousMode"] != sites.DeploymentModeStandard {
		t.Fatalf("metadata = %+v", recorded.Metadata)
	}
	if recorded.RemoteAddress != "203.0.113.9" {
		t.Fatalf("remote = %q", recorded.RemoteAddress)
	}
}

// A site mid-operation gets the settings editor's own refusal: the switch
// re-renders the vhost and must not interleave with an in-flight job.
func TestSwitchModeRefusesASiteThatIsMidOperation(t *testing.T) {
	module, _, _, _, _ := newDeployModule(t)
	catalog := module.sites.(*fakeCatalog)
	for _, status := range []sites.Status{sites.StatusPlanning, sites.StatusActivating, sites.StatusRollingBack, sites.StatusDeleting} {
		site := standardSite()
		site.Status = status
		actor := "user_admin"
		_, err := module.switchMode(context.Background(), site, sites.DeploymentModeDeployer, &actor, "")
		var refused *refusal
		if !errors.As(err, &refused) || refused.code != "site_busy" {
			t.Fatalf("switchMode(%s) err = %v, want site_busy", status, err)
		}
	}
	if len(catalog.modes) != 0 {
		t.Fatalf("modes = %v, want nothing stored", catalog.modes)
	}
}

func TestSwitchModeRejectsAModeTheRendererDoesNotKnow(t *testing.T) {
	module, _, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	_, err := module.switchMode(context.Background(), standardSite(), "kubernetes", &actor, "")
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "invalid_deployment_mode" {
		t.Fatalf("switchMode() err = %v, want invalid_deployment_mode", err)
	}
}

// A switch that cannot be recorded is refused: it re-points a live document
// root, and an unattributable one is not a change this module accepts.
func TestSwitchModeRefusesAnUnauditableChange(t *testing.T) {
	module, _, _, _, database := newDeployModule(t)
	catalog := module.sites.(*fakeCatalog)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	actor := "user_admin"
	if _, err := module.switchMode(context.Background(), standardSite(), sites.DeploymentModeDeployer, &actor, ""); !errors.Is(err, audit.ErrUnauditable) {
		t.Fatalf("switchMode() err = %v, want audit.ErrUnauditable", err)
	}
	if len(catalog.modes) != 0 {
		t.Fatalf("modes = %v, want nothing stored", catalog.modes)
	}
}

func TestSharedEnvReadsTheDocumentFromTheNode(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.env, operator.envPresent = "APP_KEY=base64:abc\n", true
	shared, err := module.sharedEnv(context.Background(), deployerSite)
	if err != nil {
		t.Fatalf("sharedEnv() = %v, want nil", err)
	}
	if shared.Content != operator.env || shared.Bytes != len(operator.env) || !shared.Present {
		t.Fatalf("shared = %+v", shared)
	}
	if shared.ModifiedAt == nil || shared.SiteID != testSite.ID {
		t.Fatalf("shared = %+v", shared)
	}
	if len(operator.envReads) != 1 || operator.envReads[0].UnixUser != testSite.UnixUser {
		t.Fatalf("reads = %+v", operator.envReads)
	}
}

// The audit entry answers "who changed it, how big was it, and is it the same
// document as last time" — and nothing else. A secret store that leaks through
// its own audit trail is no better than one that is not audited at all.
func TestWriteSharedEnvAuditsTheDigestAndNeverTheContent(t *testing.T) {
	module, operator, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	content := "DB_PASSWORD=hunter2\nAPI_TOKEN=sk-live-secret\n"
	shared, err := module.writeSharedEnv(context.Background(), deployerSite, content, &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("writeSharedEnv() = %v, want nil", err)
	}
	if shared.Content != content || len(operator.envWrites) != 1 || operator.envWrites[0] != content {
		t.Fatalf("shared = %+v, writes = %q", shared, operator.envWrites)
	}
	recorded := findEvent(t, auditLog, "deploy.env_updated")
	if recorded.Metadata["bytes"] != float64(len(content)) {
		t.Fatalf("bytes = %v", recorded.Metadata["bytes"])
	}
	digest, _ := recorded.Metadata["sha256"].(string)
	if len(digest) != 64 {
		t.Fatalf("sha256 = %q", digest)
	}
	for key, value := range recorded.Metadata {
		if text, ok := value.(string); ok && strings.Contains(text, "hunter2") {
			t.Fatalf("audit metadata %q carries the document content: %q", key, text)
		}
	}
}

func TestWriteSharedEnvRefusesAnUnauditableChange(t *testing.T) {
	module, operator, _, _, database := newDeployModule(t)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	actor := "user_admin"
	if _, err := module.writeSharedEnv(context.Background(), deployerSite, "A=1\n", &actor, ""); !errors.Is(err, audit.ErrUnauditable) {
		t.Fatalf("writeSharedEnv() err = %v, want audit.ErrUnauditable", err)
	}
	if len(operator.envWrites) != 0 {
		t.Fatalf("an unauditable write reached the node anyway: %q", operator.envWrites)
	}
}

// The cap and the NUL refusal are enforced in the request that typed the
// document, not a round trip later, and neither reaches the node.
func TestWriteSharedEnvRefusesAnOversizedOrNULBearingDocument(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	for name, content := range map[string]string{
		"oversized": strings.Repeat("A", 64*1024+1),
		"nul":       "APP_KEY=a\x00b\n",
	} {
		_, err := module.writeSharedEnv(context.Background(), deployerSite, content, &actor, "")
		var refused *refusal
		if !errors.As(err, &refused) || refused.code != "invalid_shared_env" {
			t.Fatalf("writeSharedEnv(%s) err = %v, want invalid_shared_env", name, err)
		}
	}
	if len(operator.envWrites) != 0 {
		t.Fatalf("writes = %q, want nothing on the node", operator.envWrites)
	}
}

// A site switched to deployer mode whose node has not been re-applied yet has
// no release tree; the node says so and the panel turns that into guidance
// rather than into an opaque failure.
func TestSharedEnvReportsAReleaseTreeTheNodeDoesNotHaveYet(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.envErr = deployoperator.ErrSharedEnvMissing
	_, err := module.sharedEnv(context.Background(), deployerSite)
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "shared_env_missing" {
		t.Fatalf("sharedEnv() err = %v, want shared_env_missing", err)
	}
}

// A standard-mode site has no shared directory: the read is refused with the
// mode as the reason, and the node is never asked.
func TestSharedEnvRefusesASiteInStandardMode(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	_, readErr := module.sharedEnv(context.Background(), standardSite())
	_, writeErr := module.writeSharedEnv(context.Background(), standardSite(), "A=1\n", &actor, "")
	for _, err := range []error{readErr, writeErr} {
		var refused *refusal
		if !errors.As(err, &refused) || refused.code != "not_deployer_mode" {
			t.Fatalf("err = %v, want not_deployer_mode", err)
		}
	}
	if len(operator.envReads) != 0 || len(operator.envWrites) != 0 {
		t.Fatalf("the node was asked anyway: %+v %q", operator.envReads, operator.envWrites)
	}
}
