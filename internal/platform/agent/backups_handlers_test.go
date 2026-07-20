package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

type backupBoundaryFake struct {
	runCalls     int
	restoreCalls int
	deleteCalls  int
}

func (*backupBoundaryFake) TestAccount(context.Context, backupoperator.Account) (backupoperator.TestResult, error) {
	return backupoperator.TestResult{OK: true}, nil
}

func (f *backupBoundaryFake) Run(context.Context, backupoperator.RunRequest) (backupoperator.Manifest, error) {
	f.runCalls++
	return backupoperator.Manifest{}, nil
}

func (f *backupBoundaryFake) Restore(context.Context, backupoperator.RestoreRequest) error {
	f.restoreCalls++
	return nil
}

func (f *backupBoundaryFake) DeleteCopy(context.Context, backupoperator.DeleteRequest) error {
	f.deleteCalls++
	return nil
}

func (*backupBoundaryFake) InstallSchedule(context.Context, backupoperator.ScheduleSpec) error {
	return nil
}
func (*backupBoundaryFake) RemoveSchedule(context.Context, string) error { return nil }

func TestBackupRunHTTPRejectsClientControlledFilesystemPaths(t *testing.T) {
	operator := new(backupBoundaryFake)
	server := &Server{backups: operator}
	request := backupoperator.RunRequest{
		Account: backupoperator.Account{Type: backupoperator.TypeLocal, Path: "/srv/backups"},
		PlanID:  "bkplan_1", CopyName: "copy_1", Limit: 3, StagingRoot: "/",
		Sites: []backupoperator.SiteTarget{{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"}},
	}
	response := serveBackupJSON(t, server.backupRunHTTP, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusUnprocessableEntity, response.Body.String())
	}
	if operator.runCalls != 0 {
		t.Fatalf("operator Run calls = %d, want none for an invalid request", operator.runCalls)
	}
}

func TestBackupRestoreHTTPRejectsDestinationOutsideManagedSites(t *testing.T) {
	operator := new(backupBoundaryFake)
	server := &Server{backups: operator}
	request := backupoperator.RestoreRequest{
		Account: backupoperator.Account{Type: backupoperator.TypeLocal, Path: "/srv/backups"},
		PlanID:  "bkplan_1", CopyName: "copy_1",
		Sites: []backupoperator.SiteRestoreTarget{{
			Entry: "site-blog.tar.gz", Clear: true,
			Target: backupoperator.SiteTarget{Slug: "blog", RootPath: "/", UnixUser: "root"},
		}},
	}
	response := serveBackupJSON(t, server.backupRestoreHTTP, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusUnprocessableEntity, response.Body.String())
	}
	if operator.restoreCalls != 0 {
		t.Fatalf("operator Restore calls = %d, want none for an invalid request", operator.restoreCalls)
	}
}

func TestBackupDeleteHTTPRejectsPlanTraversal(t *testing.T) {
	operator := new(backupBoundaryFake)
	server := &Server{backups: operator}
	request := backupoperator.DeleteRequest{
		Account: backupoperator.Account{Type: backupoperator.TypeLocal, Path: "/srv/backups"},
		PlanID:  "../other-plan", CopyName: "copy_1",
	}
	response := serveBackupJSON(t, server.backupDeleteCopyHTTP, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusUnprocessableEntity, response.Body.String())
	}
	if operator.deleteCalls != 0 {
		t.Fatalf("operator DeleteCopy calls = %d, want none for an invalid request", operator.deleteCalls)
	}
}

func serveBackupJSON(t *testing.T, handler http.HandlerFunc, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	handler(response, request)
	return response
}
