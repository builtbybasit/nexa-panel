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
	expected := map[string]map[Permission]bool{
		"viewer": {
			SystemRead: true, JobsRead: true, RuntimesRead: true, SitesRead: true,
			DomainsRead: true, CertificatesRead: true, DatabasesRead: true,
			FilesRead: true, LogsRead: true, SchedulesRead: true,
			ApplicationsRead: true, BackupsRead: true, ServicesRead: true, FirewallRead: true,
			DeployRead: true,
		},
		"developer": {
			SystemRead: true, JobsRead: true, RuntimesRead: true, SitesRead: true,
			FilesRead: true, FilesWrite: true, LogsRead: true,
			SchedulesRead: true, SchedulesWrite: true,
			ApplicationsRead: true, BackupsRead: true,
			DeployRead: true, DeployWrite: true,
		},
		"operator": {
			SystemRead: true, JobsRead: true, RuntimesRead: true, SitesRead: true, SitesWrite: true,
			DomainsRead: true, DomainsWrite: true, CertificatesRead: true, CertificatesWrite: true,
			DatabasesRead: true, DatabasesWrite: true,
			FilesRead: true, FilesWrite: true, LogsRead: true, SchedulesRead: true, SchedulesWrite: true,
			ApplicationsRead: true, ApplicationsWrite: true, BackupsRead: true, BackupsWrite: true,
			ServicesRead: true, ServicesWrite: true, FirewallRead: true, FirewallWrite: true,
			DeployRead: true, DeployWrite: true,
		},
		"admin": {
			SystemRead: true, SystemUpdate: true, JobsRead: true, AuditRead: true, RuntimesRead: true, SitesRead: true, SitesWrite: true,
			DomainsRead: true, DomainsWrite: true, CertificatesRead: true, CertificatesWrite: true,
			DatabasesRead: true, DatabasesWrite: true, OperationsApply: true,
			FilesRead: true, FilesWrite: true, LogsRead: true, SchedulesRead: true, SchedulesWrite: true,
			ApplicationsRead: true, ApplicationsWrite: true, BackupsRead: true, BackupsWrite: true,
			ServicesRead: true, ServicesWrite: true, FirewallRead: true, FirewallWrite: true,
			DeployRead: true, DeployWrite: true, UsersManage: true,
		},
		"unknown": {},
	}
	// Iterate the canonical AllPermissions so every recognized permission is
	// asserted for every role; a permission left out of an expected map is
	// treated as "must be denied".
	for role, grants := range expected {
		for _, permission := range AllPermissions {
			if got := policy.Allowed(role, permission); got != grants[permission] {
				t.Errorf("Allowed(%q, %q) = %v, want %v", role, permission, got, grants[permission])
			}
		}
	}
}

// TestAllPermissionsCoversGrants guards the matrix above: every permission any
// role is actually granted must appear in AllPermissions, so a newly added and
// granted permission can never be silently skipped by the matrix iteration.
func TestAllPermissionsCoversGrants(t *testing.T) {
	known := make(map[Permission]struct{}, len(AllPermissions))
	for _, p := range AllPermissions {
		known[p] = struct{}{}
	}
	for role, grants := range New().grants {
		for permission := range grants {
			if _, ok := known[permission]; !ok {
				t.Errorf("role %q is granted %q, which is absent from AllPermissions", role, permission)
			}
		}
	}
}
