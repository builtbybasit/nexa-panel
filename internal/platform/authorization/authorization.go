package authorization

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

type Permission string

const (
	SystemRead        Permission = "system.read"
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
	OperationsPlan    Permission = "operations.plan"
	OperationsApply   Permission = "operations.apply"
)

type Policy struct {
	grants map[string]map[Permission]struct{}
}

func New() *Policy {
	return &Policy{grants: map[string]map[Permission]struct{}{
		"viewer": {
			SystemRead: {}, JobsRead: {}, RuntimesRead: {}, SitesRead: {}, DomainsRead: {}, CertificatesRead: {}, DatabasesRead: {},
		},
		"operator": {
			SystemRead: {}, JobsRead: {}, RuntimesRead: {}, SitesRead: {}, SitesWrite: {}, DomainsRead: {}, DomainsWrite: {}, CertificatesRead: {}, CertificatesWrite: {}, DatabasesRead: {}, DatabasesWrite: {}, OperationsPlan: {},
		},
		"admin": {
			SystemRead: {}, JobsRead: {}, AuditRead: {}, RuntimesRead: {}, SitesRead: {}, SitesWrite: {}, DomainsRead: {}, DomainsWrite: {}, CertificatesRead: {}, CertificatesWrite: {}, DatabasesRead: {}, DatabasesWrite: {}, OperationsPlan: {}, OperationsApply: {},
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
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + message + `"}` + "\n"))
}
