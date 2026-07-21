package authorization

import "testing"

func TestRolePermissions(t *testing.T) {
	policy := New()
	tests := []struct {
		role       string
		permission Permission
		allowed    bool
	}{
		{"viewer", SystemRead, true},
		{"viewer", OperationsApply, false},
		{"operator", OperationsApply, false},
		{"admin", OperationsApply, true},
		{"unknown", SystemRead, false},
	}
	for _, test := range tests {
		if got := policy.Allowed(test.role, test.permission); got != test.allowed {
			t.Fatalf("Allowed(%q, %q) = %v, want %v", test.role, test.permission, got, test.allowed)
		}
	}
}

func TestRolePermissionMatrix(t *testing.T) {
	policy := New()
	permissions := []Permission{
		SystemRead, SystemUpdate, JobsRead, AuditRead, RuntimesRead, SitesRead, SitesWrite,
		DomainsRead, DomainsWrite, CertificatesRead, CertificatesWrite,
		DatabasesRead, DatabasesWrite, OperationsApply,
		FilesRead, FilesWrite, LogsRead, SchedulesRead, SchedulesWrite, UsersManage,
	}
	expected := map[string]map[Permission]bool{
		"viewer": {
			SystemRead: true, JobsRead: true, RuntimesRead: true, SitesRead: true,
			DomainsRead: true, CertificatesRead: true, DatabasesRead: true,
			FilesRead: true, LogsRead: true, SchedulesRead: true,
		},
		"developer": {
			SystemRead: true, JobsRead: true, RuntimesRead: true, SitesRead: true,
			FilesRead: true, FilesWrite: true, LogsRead: true,
			SchedulesRead: true, SchedulesWrite: true,
		},
		"operator": {
			SystemRead: true, JobsRead: true, RuntimesRead: true, SitesRead: true, SitesWrite: true,
			DomainsRead: true, DomainsWrite: true, CertificatesRead: true, CertificatesWrite: true,
			DatabasesRead: true, DatabasesWrite: true,
			FilesRead: true, FilesWrite: true, LogsRead: true, SchedulesRead: true, SchedulesWrite: true,
		},
		"admin": {
			SystemRead: true, SystemUpdate: true, JobsRead: true, AuditRead: true, RuntimesRead: true, SitesRead: true, SitesWrite: true,
			DomainsRead: true, DomainsWrite: true, CertificatesRead: true, CertificatesWrite: true,
			DatabasesRead: true, DatabasesWrite: true, OperationsApply: true,
			FilesRead: true, FilesWrite: true, LogsRead: true, SchedulesRead: true, SchedulesWrite: true,
			UsersManage: true,
		},
		"unknown": {},
	}
	for role, grants := range expected {
		for _, permission := range permissions {
			if got := policy.Allowed(role, permission); got != grants[permission] {
				t.Errorf("Allowed(%q, %q) = %v, want %v", role, permission, got, grants[permission])
			}
		}
	}
}
