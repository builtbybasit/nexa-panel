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
		{"viewer", OperationsPlan, false},
		{"operator", OperationsPlan, true},
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
