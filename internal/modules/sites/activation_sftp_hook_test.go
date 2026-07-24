package sites

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

type recordingProvisioner struct {
	siteIDs []string
	applied bool
	err     error
}

func (p *recordingProvisioner) ProvisionPendingCredentials(_ context.Context, siteID string) (bool, error) {
	p.siteIDs = append(p.siteIDs, siteID)
	return p.applied, p.err
}

// planReadySite builds a module with a planned site and returns the module and
// the persisted plan payload, exactly as the activate job would receive it.
func planReadySite(t *testing.T) (*Module, json.RawMessage, string) {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, fakeSiteOperator{})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)
	site, job, err := module.Create(ctx, CreateRequest{Slug: "hooked", DisplayName: "Hooked", PrimaryDomain: "hooked.example.com", PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	plan, _, err := module.Plan(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return module, encoded, site.ID
}

// Activation is the moment staged SFTP credentials can first work, so the hook
// must run exactly then, with the activated site's id.
func TestActivateJobProvisionsStagedSftpCredentials(t *testing.T) {
	module, plan, siteID := planReadySite(t)
	provisioner := &recordingProvisioner{applied: true}
	module.SetSftpProvisioner(provisioner)
	messages := make([]string, 0)
	report := func(_ int, message string) error {
		messages = append(messages, message)
		return nil
	}
	if _, err := module.activateJob(context.Background(), plan, report); err != nil {
		t.Fatalf("activateJob() = %v, want nil", err)
	}
	if len(provisioner.siteIDs) != 1 || provisioner.siteIDs[0] != siteID {
		t.Fatalf("provisioner calls = %v, want exactly the activated site", provisioner.siteIDs)
	}
	if !strings.Contains(strings.Join(messages, "\n"), "SFTP access enabled") {
		t.Fatalf("messages = %v, want the applied credentials narrated", messages)
	}
}

// The site itself is live once Apply succeeded; a staging failure is job-log
// news, never grounds to fail the activation or mark the site failed.
func TestActivateJobSurvivesASftpProvisioningFailure(t *testing.T) {
	module, plan, siteID := planReadySite(t)
	provisioner := &recordingProvisioner{err: errors.New("chroot not ready")}
	module.SetSftpProvisioner(provisioner)
	messages := make([]string, 0)
	report := func(_ int, message string) error {
		messages = append(messages, message)
		return nil
	}
	if _, err := module.activateJob(context.Background(), plan, report); err != nil {
		t.Fatalf("activateJob() = %v, want nil despite the SFTP failure", err)
	}
	site, err := module.Get(context.Background(), siteID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Status != StatusActive {
		t.Fatalf("site status = %s, want active — SFTP trouble must not undo an activation", site.Status)
	}
	if !strings.Contains(strings.Join(messages, "\n"), "could not be applied") {
		t.Fatalf("messages = %v, want the failure narrated", messages)
	}
}
