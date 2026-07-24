package sftp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"

	"golang.org/x/crypto/bcrypt"
)

func postCredentials(module *Module, site sites.Site, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/sites/"+site.ID+"/sftp/credentials", strings.NewReader(body))
	module.stageCredentials(recorder, request, site)
	return recorder
}

// pendingHash reads the staged hash; a site with no sftp_access row at all has
// nothing staged, which reads as nil like an explicit NULL does.
func pendingHash(t *testing.T, module *Module, siteID string) *string {
	t.Helper()
	row := new(accessModel)
	err := module.database.NewSelect().Model(row).Where("site_id = ?", siteID).Scan(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("read sftp_access row: %v", err)
	}
	return row.PendingHash
}

// Staging never touches the node and never stores the plaintext: only a bcrypt
// hash of the chosen password may land in the row, waiting for activation.
func TestStageCredentialsStoresOnlyAHash(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	planned := testSite
	planned.Status = sites.StatusPlanReady
	recorder := postCredentials(module, planned, `{"password":"chosen-by-the-wizard"}`)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["pendingActivation"] != true {
		t.Fatalf("body = %+v, want pendingActivation true", body)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("staging must not drive the node, got %+v", operator.applied)
	}
	hash := pendingHash(t, module, testSite.ID)
	if hash == nil || *hash == "" {
		t.Fatal("no pending hash was stored")
	}
	if strings.Contains(*hash, "chosen-by-the-wizard") {
		t.Fatal("the stored value contains the plaintext password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*hash), []byte("chosen-by-the-wizard")); err != nil {
		t.Fatalf("stored hash does not verify the chosen password: %v", err)
	}
	enabled, err := module.AccessEnabled(context.Background(), testSite.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("staging must not report SFTP as enabled before activation")
	}
}

// An active site already has its account; the synchronous enable path (which
// never persists anything) is the right tool there, so staging is refused.
func TestStageCredentialsRefusesActiveSites(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	active := testSite
	active.Status = sites.StatusActive
	recorder := postCredentials(module, active, `{"password":"chosen-by-the-wizard"}`)
	if recorder.Code != 409 {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if len(operator.applied) != 0 {
		t.Fatalf("a refused staging drove the node: %+v", operator.applied)
	}
	if pending := pendingHash(t, module, testSite.ID); pending != nil {
		t.Fatal("a refused staging stored a hash")
	}
}

func TestStageCredentialsValidatesThePassword(t *testing.T) {
	module, _, _ := newSFTPModule(t)
	planned := testSite
	planned.Status = sites.StatusPlanReady
	for name, body := range map[string]string{
		"missing":   `{}`,
		"too short": `{"password":"short"}`,
		"too long":  `{"password":"` + strings.Repeat("x", 80) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := postCredentials(module, planned, body)
			if recorder.Code != 400 && recorder.Code != 422 {
				t.Fatalf("status = %d, want a validation refusal", recorder.Code)
			}
		})
	}
}

// Activation is the first moment the site's account exists, so this is where a
// staged credential turns into a live jail: the hash travels to the operator,
// the row flips to enabled, and the hash is destroyed.
func TestProvisionPendingCredentialsAppliesAndClearsTheHash(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	planned := testSite
	planned.Status = sites.StatusPlanReady
	if recorder := postCredentials(module, planned, `{"password":"chosen-by-the-wizard"}`); recorder.Code != 200 {
		t.Fatalf("stage status = %d, want 200", recorder.Code)
	}
	staged := *pendingHash(t, module, testSite.ID)
	applied, err := module.ProvisionPendingCredentials(context.Background(), testSite.ID)
	if err != nil {
		t.Fatalf("ProvisionPendingCredentials() = %v, want nil", err)
	}
	if !applied {
		t.Fatal("ProvisionPendingCredentials() = false, want true with a staged hash")
	}
	if len(operator.applied) != 1 {
		t.Fatalf("operator calls = %+v, want exactly one", operator.applied)
	}
	request := operator.applied[0]
	if !request.Enabled || request.PasswordHash != staged || request.Password != "" {
		t.Fatalf("operator request = %+v, want an enable carrying the staged hash and no plaintext", request)
	}
	if pending := pendingHash(t, module, testSite.ID); pending != nil {
		t.Fatal("the staged hash must be destroyed once applied")
	}
	enabled, err := module.AccessEnabled(context.Background(), testSite.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("a provisioned staging was not recorded as enabled")
	}
}

// Nothing staged means nothing to do — activation must not enable SFTP for
// sites that never asked for it.
func TestProvisionPendingCredentialsIsANoOpWithoutAStagedHash(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	applied, err := module.ProvisionPendingCredentials(context.Background(), testSite.ID)
	if err != nil {
		t.Fatalf("ProvisionPendingCredentials() = %v, want nil", err)
	}
	if applied {
		t.Fatal("ProvisionPendingCredentials() = true, want false with nothing staged")
	}
	if len(operator.applied) != 0 {
		t.Fatalf("the node was driven with nothing staged: %+v", operator.applied)
	}
}

// The SSH mutual exclusion holds for staged credentials too: applying a jail
// next to an SSH-access block would hand out a credential that cannot work.
func TestProvisionPendingCredentialsRefusesWhileSSHAccessIsEnabled(t *testing.T) {
	module, operator, state := newSFTPModule(t)
	planned := testSite
	planned.Status = sites.StatusPlanReady
	if recorder := postCredentials(module, planned, `{"password":"chosen-by-the-wizard"}`); recorder.Code != 200 {
		t.Fatalf("stage status = %d, want 200", recorder.Code)
	}
	state.enabled = true
	if _, err := module.ProvisionPendingCredentials(context.Background(), testSite.ID); err == nil {
		t.Fatal("ProvisionPendingCredentials() = nil, want a refusal while SSH access is on")
	}
	if len(operator.applied) != 0 {
		t.Fatalf("the node was driven despite the conflict: %+v", operator.applied)
	}
	if pending := pendingHash(t, module, testSite.ID); pending == nil {
		t.Fatal("a refused provisioning must keep the staged hash for the next activation")
	}
}

// A direct enable or disable supersedes whatever was staged, so the hash goes.
func TestDirectProvisioningClearsAStagedHash(t *testing.T) {
	module, _, _ := newSFTPModule(t)
	planned := testSite
	planned.Status = sites.StatusPlanReady
	if recorder := postCredentials(module, planned, `{"password":"chosen-by-the-wizard"}`); recorder.Code != 200 {
		t.Fatalf("stage status = %d, want 200", recorder.Code)
	}
	recorder := httptest.NewRecorder()
	module.provision(recorder, httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", nil), testSite, true)
	if recorder.Code != 200 {
		t.Fatalf("enable status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if pending := pendingHash(t, module, testSite.ID); pending != nil {
		t.Fatal("a direct enable must destroy the staged hash it supersedes")
	}
}

// Enable accepts a caller-chosen password (FastPanel parity) and still refuses
// a trivial one.
func TestEnableAcceptsACallerChosenPassword(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", strings.NewReader(`{"password":"my-own-strong-password"}`))
	module.provision(recorder, request, testSite, true)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if len(operator.applied) != 1 || operator.applied[0].Password != "my-own-strong-password" {
		t.Fatalf("operator calls = %+v, want the chosen password applied", operator.applied)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["password"] != "my-own-strong-password" {
		t.Fatalf("body = %+v, want the chosen password echoed once", body)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", strings.NewReader(`{"password":"short"}`))
	module.provision(recorder, request, testSite, true)
	if recorder.Code != 422 {
		t.Fatalf("status = %d, want 422 for a trivial password", recorder.Code)
	}
}
