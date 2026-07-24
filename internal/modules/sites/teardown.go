package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"

	"github.com/uptrace/bun"
)

// deletePayload is the durable request for a site teardown. TeardownHost is
// captured at request time (only an active site has managed configuration on the
// node) so a retried job keeps deciding correctly after the row moves to the
// transient "deleting" status.
type deletePayload struct {
	SiteID       string `json:"siteId"`
	TeardownHost bool   `json:"teardownHost"`
}

// deleteHTTP begins a durable site teardown. It blocks (409) while the site is
// mid-operation, or while dependents that carry their own node state — extra
// domains or a live TLS certificate — are still attached, so the node is never
// left with orphaned Nginx or Let's Encrypt configuration.
func (m *Module) deleteHTTP(w http.ResponseWriter, r *http.Request) {
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	if !siteDeletable(site.Status) {
		httpapi.WriteError(w, http.StatusConflict, "site_busy", "The site is mid-operation; wait for the current job to finish before deleting it.")
		return
	}
	if blocker, err := m.dependentBlocker(r.Context(), site.ID); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site dependents could not be checked.")
		return
	} else if blocker != "" {
		httpapi.WriteError(w, http.StatusConflict, "site_has_dependents", blocker)
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	payload := deletePayload{SiteID: site.ID, TeardownHost: site.Status == StatusActive}
	// Fail-closed, and ahead of the submit: a teardown destroys the site's files,
	// system user, and Nginx configuration, so it must not be queued at all if it
	// cannot be attributed in the audit log.
	if err := m.jobs.Audit().RecordSensitive(r.Context(), audit.Entry{
		ActorUserID: &user.ID, Action: "site.deleted", Subject: "site:" + site.ID,
		RemoteAddress: httpapi.RemoteAddress(r),
		Metadata: map[string]any{
			"slug": site.Slug, "displayName": site.DisplayName, "primaryDomain": site.PrimaryDomain,
			"teardownHost": payload.TeardownHost,
			"before":       map[string]any{"status": string(site.Status)},
			"after":        map[string]any{"status": string(StatusDeleting)},
		},
	}); err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "audit_unavailable", "The removal was refused because it could not be recorded in the audit log.")
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "site.delete", "Delete site "+site.DisplayName, payload, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "Site removal could not be queued.")
		return
	}
	_, err = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusDeleting).Set("last_job_id = ?", job.ID).Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).Where("id = ?", site.ID).Exec(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_update_failed", "The queued removal could not be attached to the site.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}

// siteDeletable rejects a teardown while the site is mid-operation, so a delete
// cannot interleave with an in-flight plan, activation, or rollback.
func siteDeletable(status Status) bool {
	switch status {
	case StatusActive, StatusDraft, StatusPlanReady, StatusRolledBack, StatusFailed:
		return true
	default:
		return false
	}
}

// dependentBlocker returns a descriptive message when the site still owns
// resources that must be removed through their own node-aware teardown first,
// or an empty string when the site is free to delete. This includes database
// references whose rows do not cascade: retaining either would create a task or
// backup plan that names a site which no longer exists.
func (m *Module) dependentBlocker(ctx context.Context, siteID string) (string, error) {
	hostnames := make([]string, 0)
	if err := m.database.NewSelect().TableExpr("domains").Column("hostname").
		Where("site_id = ?", siteID).Where("kind <> ?", "primary").
		OrderExpr("hostname ASC").Scan(ctx, &hostnames); err != nil {
		return "", err
	}
	certificates, err := m.database.NewSelect().TableExpr("certificates").
		Where("site_id = ?", siteID).Where("status NOT IN (?)", bun.List([]string{"revoked", "failed"})).Count(ctx)
	if err != nil {
		return "", err
	}
	// SSH access lives on the node as an sshd drop-in, an authorized-keys file,
	// and an interactive login shell. The site_ssh_access row cascades away with
	// the site, so deleting while it is enabled would strand all three and leave
	// a shell account for a site that no longer exists.
	sshEnabled, err := m.database.NewSelect().TableExpr("site_ssh_access").
		Where("site_id = ?", siteID).Where("enabled = ?", true).Count(ctx)
	if err != nil {
		return "", err
	}
	// SFTP strands the same class of state: an sshd drop-in the site teardown
	// does not remove, plus a live password in /etc/shadow on an account nothing
	// ever deletes. A later site reusing the slug would inherit both.
	sftpEnabled, err := m.database.NewSelect().TableExpr("sftp_access").
		Where("site_id = ?", siteID).Where("enabled = ?", true).Count(ctx)
	if err != nil {
		return "", err
	}
	scheduledTasks := make([]string, 0)
	if err := m.database.NewSelect().TableExpr("scheduled_tasks").Column("name").
		Where("site_id = ?", siteID).OrderExpr("name ASC").Scan(ctx, &scheduledTasks); err != nil {
		return "", err
	}
	// backup_plan_sites is the trigger-maintained relation behind the plans'
	// site_ids JSON. Joining it replaces a full scan-and-decode of every plan row
	// and, more importantly, it is the same relation the database trigger refuses
	// a site deletion on, so the message a caller gets and the constraint that
	// would fire cannot disagree.
	backupPlans := make([]string, 0)
	if err := m.database.NewSelect().TableExpr("backup_plans AS plan").Column("plan.name").
		Join("JOIN backup_plan_sites AS target ON target.plan_id = plan.id").
		Where("target.site_id = ?", siteID).OrderExpr("plan.name ASC").Scan(ctx, &backupPlans); err != nil {
		return "", err
	}
	// Stored copies are keyed on the plan, never on the site, so they are counted
	// through the same relation. A plan holding copies cannot be deleted (see
	// backup_plans_require_no_copies), which means the copies decide whether the
	// plan reference above can be cleared at all — saying so turns an otherwise
	// circular refusal into an actionable one.
	backupCopies, err := m.database.NewSelect().TableExpr("backup_copies AS copy").
		Join("JOIN backup_plan_sites AS target ON target.plan_id = copy.plan_id").
		Where("target.site_id = ?", siteID).Count(ctx)
	if err != nil {
		return "", err
	}
	// Databases are the site's own data, not a rendered artifact, so a teardown
	// never removes them: it reports them and leaves the operator to decide.
	// What changed is how they are found — site_id, the relation the database
	// trigger also refuses the site deletion on, rather than a guess at the name.
	databases, err := m.databasesOwnedBySite(ctx, siteID)
	if err != nil {
		return "", err
	}
	blockers := make([]string, 0, 8)
	if len(hostnames) > 0 {
		blockers = append(blockers, fmt.Sprintf("remove its attached domains first (%s)", strings.Join(hostnames, ", ")))
	}
	if certificates > 0 {
		blockers = append(blockers, "remove its TLS certificate first")
	}
	if sshEnabled > 0 {
		blockers = append(blockers, "disable its SSH access first")
	}
	if sftpEnabled > 0 {
		blockers = append(blockers, "disable its SFTP access first")
	}
	if len(scheduledTasks) > 0 {
		blockers = append(blockers, fmt.Sprintf("remove its scheduled tasks first (%s)", strings.Join(scheduledTasks, ", ")))
	}
	if len(backupPlans) > 0 {
		blockers = append(blockers, fmt.Sprintf("remove it from backup plans first (%s)", strings.Join(backupPlans, ", ")))
	}
	if backupCopies > 0 {
		blockers = append(blockers, fmt.Sprintf("delete the %d stored backup copies taken for it first", backupCopies))
	}
	if len(databases) > 0 {
		blockers = append(blockers, fmt.Sprintf("delete the databases it owns first (%s)", strings.Join(databases, ", ")))
	}
	if len(blockers) == 0 {
		return "", nil
	}
	return "The site cannot be deleted yet: " + strings.Join(blockers, "; ") + ".", nil
}

// databasesOwnedBySite reports the managed PostgreSQL and MySQL databases the
// site owns. Both engines' tables carry site_id, recorded when the database is
// created for a site and backfilled for older rows from the "nexa_<slug>"
// account-name convention this replaced — so a database named anything at all
// is still visible here, which the name match could never promise.
func (m *Module) databasesOwnedBySite(ctx context.Context, siteID string) ([]string, error) {
	names := make([]string, 0)
	for _, table := range []string{"managed_databases", "mysql_databases"} {
		found := make([]string, 0)
		if err := m.database.NewSelect().TableExpr(table).Column("name").
			Where("site_id = ?", siteID).OrderExpr("name ASC").Scan(ctx, &found); err != nil {
			return nil, err
		}
		names = append(names, found...)
	}
	return names, nil
}

// deleteJob removes the managed Nginx and PHP-FPM configuration from the node and
// then deletes the site record. Its primary domain, stored plan, and any cascaded
// rows are removed by the foreign-key cascade once the row is gone.
func (m *Module) deleteJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload deletePayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.SiteID == "" {
		return nil, errors.New("invalid persisted site removal request")
	}
	if err := report(15, "Loading the site marked for removal."); err != nil {
		return nil, err
	}
	site, err := m.Get(ctx, payload.SiteID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"siteId": payload.SiteID, "removed": true, "alreadyGone": true}, nil
	}
	if err != nil {
		return nil, err
	}
	// Re-check inside the durable job. The request-time check prevents an
	// intentional unsafe delete; this check closes the interval in which an
	// already-running write could attach a dependent before the site's status
	// became deleting.
	if blocker, err := m.dependentBlocker(ctx, site.ID); err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	} else if blocker != "" {
		err := errors.New(blocker)
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	// Withdraw the deploy-side grants first. They live outside this module's
	// artifacts (/etc/sudoers.d), so removing the vhost would not touch them,
	// and a rule naming a slug that no longer exists must not survive to be
	// inherited by a site created with the same slug later.
	if m.deployTeardown != nil {
		if err := report(30, "Withdrawing this site's deployment grants."); err != nil {
			return nil, err
		}
		if err := m.deployTeardown.TeardownSiteDeployment(ctx, site.ID); err != nil {
			m.markFailed(context.WithoutCancel(ctx), site.ID, err)
			return nil, err
		}
	}
	if payload.TeardownHost {
		if err := report(45, "Removing managed Nginx and PHP-FPM configuration from the node."); err != nil {
			return nil, err
		}
		if err := m.teardownHost(ctx, site); err != nil {
			m.markFailed(context.WithoutCancel(ctx), site.ID, err)
			return nil, err
		}
	}
	// The account and the site root are purged whatever the payload says, because
	// they are not created by an activation: a site whose first activation failed
	// still has both, since Apply prepares the identity before it writes anything
	// and its own rollback only restores files. Purge is idempotent, so on a site
	// that never reached the node it does nothing.
	if err := report(70, "Removing this site's system account and files."); err != nil {
		return nil, err
	}
	if err := m.purgeHost(ctx, site); err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	if err := report(85, "Removing the site record."); err != nil {
		return nil, err
	}
	if _, err := m.database.NewDelete().Model((*siteModel)(nil)).Where("id = ?", site.ID).Exec(ctx); err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	if err := report(95, "Site configuration and record removed."); err != nil {
		return nil, err
	}
	return map[string]any{"siteId": site.ID, "removed": true}, nil
}

// teardownHost drives the node operator to strip the managed configuration. It
// reuses the activation Rollback path with a synthetic plan whose "before" state
// is empty: rolling that plan back removes the rendered artifacts and disables
// the Nginx site atomically, refusing (rather than half-applying) if the managed
// configuration has drifted since it was activated.
func (m *Module) teardownHost(ctx context.Context, site Site) error {
	// Definition threads the persisted settings, so the teardown plan renders the
	// same conditional artifacts (htpasswd, rate-limit zone) the site activated
	// with — and the synthetic rollback below removes every one of them, not just
	// the three core files.
	definition, err := m.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("assemble site teardown definition: %w", err)
	}
	// The agent issues the synthetic plan and signs it. Building this shape here
	// by editing an activation plan is what used to break teardown: the signature
	// covers the whole plan, so the agent rejected the edited copy with "The site
	// plan was not issued by this agent." Send back exactly what was issued.
	plan, err := m.operator.PlanTeardown(ctx, definition)
	if err != nil {
		return fmt.Errorf("plan site teardown: %w", err)
	}
	if _, err := m.operator.Rollback(ctx, plan); err != nil {
		return fmt.Errorf("remove managed site configuration: %w", err)
	}
	return nil
}

// purgeHost removes the host state no rendered artifact covers: the site's Unix
// account and its site root. It runs after teardownHost so the vhost is already
// gone when the files disappear, and it is a separate operator call because
// there is no before-state to snapshot — the node verifies the identity against
// the site itself and refuses anything it does not own.
func (m *Module) purgeHost(ctx context.Context, site Site) error {
	definition, err := m.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("assemble site teardown definition: %w", err)
	}
	if err := m.operator.Purge(ctx, definition); err != nil {
		return fmt.Errorf("remove site account and files: %w", err)
	}
	return nil
}
