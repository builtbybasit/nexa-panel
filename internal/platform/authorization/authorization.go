package authorization

import (
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

type Permission string

const (
	SystemRead        Permission = "system.read"
	SystemUpdate      Permission = "system.update"
	JobsRead          Permission = "jobs.read"
	AuditRead         Permission = "audit.read"
	RuntimesRead      Permission = "runtimes.read"
	SitesRead         Permission = "sites.read"
	SitesWrite        Permission = "sites.write"
	DomainsRead       Permission = "domains.read"
	DomainsWrite      Permission = "domains.write"
	CertificatesRead  Permission = "certificates.read"
	CertificatesWrite Permission = "certificates.write"
	DatabasesRead     Permission = "databases.read"
	DatabasesWrite    Permission = "databases.write"
	OperationsApply   Permission = "operations.apply"
	FilesRead         Permission = "files.read"
	FilesWrite        Permission = "files.write"
	LogsRead          Permission = "logs.read"
	SchedulesRead     Permission = "schedules.read"
	SchedulesWrite    Permission = "schedules.write"
	ApplicationsRead  Permission = "applications.read"
	ApplicationsWrite Permission = "applications.write"
	BackupsRead       Permission = "backups.read"
	BackupsWrite      Permission = "backups.write"
	ServicesRead      Permission = "services.read"
	ServicesWrite     Permission = "services.write"
	FirewallRead      Permission = "firewall.read"
	FirewallWrite     Permission = "firewall.write"
	DeployRead        Permission = "deploy.read"
	DeployWrite       Permission = "deploy.write"
	UsersManage       Permission = "users.manage"
)

type Policy struct {
	grants    map[string]map[Permission]struct{}
	now       func() time.Time
	stepUpTTL time.Duration
}

func New() *Policy {
	return &Policy{now: time.Now, stepUpTTL: 10 * time.Minute, grants: map[string]map[Permission]struct{}{
		"viewer": {
			SystemRead: {}, JobsRead: {}, RuntimesRead: {}, SitesRead: {}, DomainsRead: {}, CertificatesRead: {}, DatabasesRead: {}, FilesRead: {}, LogsRead: {}, SchedulesRead: {}, ApplicationsRead: {}, BackupsRead: {}, ServicesRead: {}, FirewallRead: {}, DeployRead: {},
		},
		"developer": {
			SystemRead: {}, JobsRead: {}, RuntimesRead: {}, SitesRead: {}, FilesRead: {}, FilesWrite: {}, LogsRead: {}, SchedulesRead: {}, SchedulesWrite: {}, ApplicationsRead: {}, BackupsRead: {}, DeployRead: {}, DeployWrite: {},
		},
		"operator": {
			SystemRead: {}, JobsRead: {}, RuntimesRead: {}, SitesRead: {}, SitesWrite: {}, DomainsRead: {}, DomainsWrite: {}, CertificatesRead: {}, CertificatesWrite: {}, DatabasesRead: {}, DatabasesWrite: {}, FilesRead: {}, FilesWrite: {}, LogsRead: {}, SchedulesRead: {}, SchedulesWrite: {}, ApplicationsRead: {}, ApplicationsWrite: {}, BackupsRead: {}, BackupsWrite: {}, ServicesRead: {}, ServicesWrite: {}, FirewallRead: {}, FirewallWrite: {}, DeployRead: {}, DeployWrite: {},
		},
		"admin": {
			SystemRead: {}, SystemUpdate: {}, JobsRead: {}, AuditRead: {}, RuntimesRead: {}, SitesRead: {}, SitesWrite: {}, DomainsRead: {}, DomainsWrite: {}, CertificatesRead: {}, CertificatesWrite: {}, DatabasesRead: {}, DatabasesWrite: {}, OperationsApply: {}, FilesRead: {}, FilesWrite: {}, LogsRead: {}, SchedulesRead: {}, SchedulesWrite: {}, ApplicationsRead: {}, ApplicationsWrite: {}, BackupsRead: {}, BackupsWrite: {}, ServicesRead: {}, ServicesWrite: {}, FirewallRead: {}, FirewallWrite: {}, DeployRead: {}, DeployWrite: {}, UsersManage: {},
		},
	}}
}

func (p *Policy) Allowed(role string, permission Permission) bool {
	_, ok := p.grants[role][permission]
	return ok
}

func (p *Policy) Middleware(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := identity.UserFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		if !p.Allowed(user.Role, Permission(permission)) {
			writeError(w, http.StatusForbidden, "permission_denied", "Your role cannot perform this action.")
			return
		}
		// Step-up applies only to accounts that opted into MFA. Because MFA is
		// optional, a password-only account would otherwise be permanently blocked
		// from sensitive actions it is authorized for (it can never satisfy
		// RecentMFA). An enrolled account still must re-confirm recently.
		if sensitivePermission(Permission(permission)) && identity.MFAEnrolled(r.Context()) && !identity.RecentMFA(r.Context(), p.now(), p.stepUpTTL) {
			writeError(w, http.StatusForbidden, "mfa_step_up_required", "Confirm multi-factor authentication again to perform this sensitive action.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sensitivePermission(permission Permission) bool {
	return permission == OperationsApply || permission == UsersManage || permission == SystemUpdate || permission == FirewallWrite || permission == DeployWrite
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	httpapi.WriteError(w, status, code, message)
}
