package backups

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

type bindingSiteResolver struct {
	target backupoperator.SiteTarget
}

func (resolver *bindingSiteResolver) BackupSite(context.Context, string) (backupoperator.SiteTarget, error) {
	return resolver.target, nil
}

type restorePreviewEnvelope struct {
	CopyID         string   `json:"copyId"`
	CopyName       string   `json:"copyName"`
	IntegrityState string   `json:"integrityState"`
	Warnings       []string `json:"warnings"`
	Items          []struct {
		Kind             string `json:"kind"`
		SourceEntry      string `json:"sourceEntry"`
		DestinationRef   string `json:"destinationRef"`
		DestinationLabel string `json:"destinationLabel"`
		Clear            bool   `json:"clear"`
		Impact           string `json:"impact"`
	} `json:"items"`
	PreviewToken string    `json:"previewToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func seedRestoreCopy(t *testing.T, module *Module) copyModel {
	t.Helper()
	account := seedAccount(t, module)
	plan, err := module.CreatePlan(context.Background(), PlanRequest{
		Name: "restore", AccountID: account.ID, CopiesLimit: 5,
		SiteIDs: []string{"site_1"}, Schedule: "0 3 * * *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := copyModel{
		ID: "bkcopy_preview", PlanID: plan.ID, AccountID: account.ID, CopyName: "2026-07-23_120000",
		RemotePath: "/srv/backups/restore", SizeBytes: 1,
		EntriesJSON: `[{"name":"site-blog.tar.gz","sizeBytes":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`,
		Status:      copyStatusUploaded, IntegrityState: integrityPassed, RestoreTestState: restoreTestNotTested,
		CreatedAt: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
	}
	if _, err := module.database.NewInsert().Model(&record).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	return record
}

func restoreRequestHTTP(t *testing.T, module *Module, handler http.HandlerFunc, copyID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backups/copies/"+copyID, strings.NewReader(body))
	request.SetPathValue("copyId", copyID)
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func responseCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, response.Body.String())
	}
	return payload.Code
}

func TestRestorePreviewIsResolvedAndHasNoMutationSideEffects(t *testing.T) {
	module, operator := newTestModule(t)
	record := seedRestoreCopy(t, module)
	installedBefore := len(operator.installed)

	response := restoreRequestHTTP(t, module, module.previewRestoreCopyHTTP, record.ID,
		`{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":true}],"databases":[],"allowUnverified":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status = %d; body=%s", response.Code, response.Body.String())
	}
	var preview restorePreviewEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.CopyID != record.ID || preview.CopyName != record.CopyName || preview.IntegrityState != integrityPassed {
		t.Fatalf("preview copy = %+v", preview)
	}
	if len(preview.Items) != 1 || preview.Items[0].Kind != "site" || preview.Items[0].SourceEntry != "site-blog.tar.gz" ||
		preview.Items[0].DestinationRef != "site_1" || preview.Items[0].DestinationLabel != "blog" ||
		!preview.Items[0].Clear || !strings.Contains(preview.Items[0].Impact, "removed") {
		t.Fatalf("preview item = %+v", preview.Items)
	}
	if preview.PreviewToken == "" || !preview.ExpiresAt.After(module.now().UTC()) {
		t.Fatalf("preview binding = token %q, expires %s", preview.PreviewToken, preview.ExpiresAt)
	}
	if strings.Contains(response.Body.String(), "/srv/nexa") || strings.Contains(response.Body.String(), "nexa_blog") {
		t.Fatalf("preview leaked privileged destination details: %s", response.Body.String())
	}
	queued, err := module.jobs.List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("preview queued jobs: %+v", queued)
	}
	if operator.lastRestore.CopyName != "" {
		t.Fatalf("preview called restore operator: %+v", operator.lastRestore)
	}
	if len(operator.installed) != installedBefore || len(operator.removed) != 0 || operator.lastRun.CopyName != "" || operator.lastDelete.CopyName != "" {
		t.Fatalf("preview caused an operator side effect: %+v", operator)
	}
}

func TestRestoreExecutionRequiresExactPreviewAndRechecksBeforeMutation(t *testing.T) {
	module, operator := newTestModule(t)
	record := seedRestoreCopy(t, module)
	resolver := &bindingSiteResolver{target: backupoperator.SiteTarget{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"}}
	module.sites = resolver
	choices := `{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":false}],"databases":[],"allowUnverified":false}`

	missing := restoreRequestHTTP(t, module, module.restoreCopyHTTP, record.ID, choices)
	if missing.Code != http.StatusConflict || responseCode(t, missing) != "backup_restore_preview_required" {
		t.Fatalf("missing preview response = %d %s", missing.Code, missing.Body.String())
	}

	previewResponse := restoreRequestHTTP(t, module, module.previewRestoreCopyHTTP, record.ID, choices)
	var preview restorePreviewEnvelope
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	changed := restoreRequestHTTP(t, module, module.restoreCopyHTTP, record.ID,
		`{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":true}],"databases":[],"allowUnverified":false,"previewToken":"`+preview.PreviewToken+`"}`)
	if changed.Code != http.StatusConflict || responseCode(t, changed) != "backup_restore_preview_changed" {
		t.Fatalf("changed preview response = %d %s", changed.Code, changed.Body.String())
	}

	exact := restoreRequestHTTP(t, module, module.restoreCopyHTTP, record.ID,
		`{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":false}],"databases":[],"allowUnverified":false,"previewToken":"`+preview.PreviewToken+`"}`)
	if exact.Code != http.StatusAccepted {
		t.Fatalf("exact restore status = %d; body=%s", exact.Code, exact.Body.String())
	}
	repeated := restoreRequestHTTP(t, module, module.restoreCopyHTTP, record.ID,
		`{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":false}],"databases":[],"allowUnverified":false,"previewToken":"`+preview.PreviewToken+`"}`)
	if repeated.Code != http.StatusAccepted {
		t.Fatalf("repeated restore status = %d; body=%s", repeated.Code, repeated.Body.String())
	}
	queued, err := module.jobs.List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued jobs = %+v", queued)
	}

	resolver.target = backupoperator.SiteTarget{Slug: "blog-new", RootPath: "/srv/nexa/sites/blog-new", UnixUser: "nexa_blog_new"}
	if _, err := module.restoreJob(context.Background(), queued[0].Request, func(int, string) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("restore job drift error = %v", err)
	}
	if operator.lastRestore.CopyName != "" {
		t.Fatalf("drifted restore called operator: %+v", operator.lastRestore)
	}
}

func TestRestorePreviewExpiresClosed(t *testing.T) {
	module, _ := newTestModule(t)
	record := seedRestoreCopy(t, module)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	module.now = func() time.Time { return now }
	choices := `{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":false}],"databases":[],"allowUnverified":false}`
	previewResponse := restoreRequestHTTP(t, module, module.previewRestoreCopyHTTP, record.ID, choices)
	var preview restorePreviewEnvelope
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	expired := restoreRequestHTTP(t, module, module.restoreCopyHTTP, record.ID,
		`{"sites":[{"entry":"site-blog.tar.gz","siteId":"site_1","clear":false}],"databases":[],"allowUnverified":false,"previewToken":"`+preview.PreviewToken+`"}`)
	if expired.Code != http.StatusConflict || responseCode(t, expired) != "backup_restore_preview_expired" {
		t.Fatalf("expired preview response = %d %s", expired.Code, expired.Body.String())
	}
}

func TestRestorePreviewRedactsDatabaseTargetAndRejectsCrossEngineRestore(t *testing.T) {
	module, operator := newTestModule(t)
	record := seedRestoreCopy(t, module)
	record.EntriesJSON = `[{"name":"db-postgres-app.dump","sizeBytes":1,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`
	if _, err := module.database.NewUpdate().Model((*copyModel)(nil)).Set("entries = ?", record.EntriesJSON).Where("id = ?", record.ID).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}

	response := restoreRequestHTTP(t, module, module.previewRestoreCopyHTTP, record.ID,
		`{"sites":[],"databases":[{"entry":"db-postgres-app.dump","databaseRef":"postgres:db_1","clear":true}],"allowUnverified":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("database preview status = %d; body=%s", response.Code, response.Body.String())
	}
	var preview restorePreviewEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || preview.Items[0].Kind != "database" || preview.Items[0].DestinationRef != "postgres:db_1" ||
		preview.Items[0].DestinationLabel != "app · PostgreSQL" || !strings.Contains(preview.Items[0].Impact, "reset") {
		t.Fatalf("database preview item = %+v", preview.Items)
	}
	if strings.Contains(response.Body.String(), "/run/postgresql") || strings.Contains(response.Body.String(), `"port"`) {
		t.Fatalf("database preview leaked connection details: %s", response.Body.String())
	}
	if operator.lastRestore.CopyName != "" {
		t.Fatalf("database preview called restore operator: %+v", operator.lastRestore)
	}

	crossEngine := restoreRequestHTTP(t, module, module.previewRestoreCopyHTTP, record.ID,
		`{"sites":[],"databases":[{"entry":"db-postgres-app.dump","databaseRef":"mysql:db_1","clear":false}],"allowUnverified":false}`)
	if crossEngine.Code != http.StatusUnprocessableEntity || responseCode(t, crossEngine) != "backup_restore_invalid_destination" {
		t.Fatalf("cross-engine response = %d %s", crossEngine.Code, crossEngine.Body.String())
	}
}
