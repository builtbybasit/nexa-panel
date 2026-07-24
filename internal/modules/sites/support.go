package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"

	"golang.org/x/crypto/bcrypt"
)

// SettingsRequest is the PATCH body. It mirrors the operator Settings except the
// basic-auth secret arrives as plaintext (write-only) and is hashed server-side;
// the hash never crosses this boundary in either direction.
type SettingsRequest struct {
	HSTS       bool  `json:"hsts"`
	HSTSMaxAge int   `json:"hstsMaxAge"`
	HTTP2      *bool `json:"http2"`
	HTTP3      bool  `json:"http3"`
	// HTTPSRedirect is a tri-state: absent keeps the forced HTTP->HTTPS 301 (the
	// default), an explicit false turns it off. The web client always sends a
	// concrete boolean, so the pointer exists mainly for API clients that PATCH a
	// partial body — and so an explicit true stays distinguishable in the stored JSON.
	HTTPSRedirect *bool `json:"httpsRedirect"`

	Subdirectory    string `json:"subdirectory"`
	Charset         string `json:"charset"`
	Gzip            *bool  `json:"gzip"`
	GzipLevel       int    `json:"gzipLevel"`
	StaticCacheDays *int   `json:"staticCacheDays"`
	ClientMaxBodyMB *int   `json:"clientMaxBodyMb"`

	PMMode        string `json:"pmMode"`
	PMMaxChildren int    `json:"pmMaxChildren"`
	PMMaxRequests *int   `json:"pmMaxRequests"`
	AccessLog     *bool  `json:"accessLog"`
	ErrorLog      *bool  `json:"errorLog"`

	// IndexFiles is the ordered `index` list; WorkingDirectory is the PHP-FPM
	// chdir, relative to the SITE ROOT (unlike Subdirectory above, which is
	// relative to public/ and selects the published document root).
	IndexFiles       []string `json:"indexFiles"`
	WorkingDirectory string   `json:"workingDirectory"`

	BasicAuth   BasicAuthRequest         `json:"basicAuth"`
	RateLimit   siteoperator.RateLimit   `json:"rateLimit"`
	LogRotation siteoperator.LogRotation `json:"logRotation"`
}

type BasicAuthRequest struct {
	Enabled  bool   `json:"enabled"`
	Realm    string `json:"realm"`
	Username string `json:"username"`
	// Password is plaintext and write-only. Blank on an enabled account keeps the
	// previously stored hash, so re-saving other fields never forces a re-entry.
	Password string `json:"password"`
}

// normalizeSettings turns a request into operator Settings, hashing a supplied
// plaintext basic-auth password (or preserving the current hash when blank) and
// validating the whole payload against the operator's bounds. The plaintext is
// discarded here and never persisted, logged, or returned.
func normalizeSettings(current siteoperator.Settings, req SettingsRequest) (siteoperator.Settings, error) {
	settings := siteoperator.Settings{
		HSTS: req.HSTS, HSTSMaxAge: req.HSTSMaxAge, HTTP2: req.HTTP2, HTTP3: req.HTTP3, HTTPSRedirect: req.HTTPSRedirect,
		Subdirectory: strings.Trim(strings.TrimSpace(req.Subdirectory), "/"),
		Charset:      strings.ToLower(strings.TrimSpace(req.Charset)),
		Gzip:         req.Gzip, GzipLevel: req.GzipLevel, StaticCacheDays: req.StaticCacheDays, ClientMaxBodyMB: req.ClientMaxBodyMB,
		PMMode: req.PMMode, PMMaxChildren: req.PMMaxChildren, PMMaxRequests: req.PMMaxRequests,
		AccessLog: req.AccessLog, ErrorLog: req.ErrorLog,
		IndexFiles:       tidyIndexFiles(req.IndexFiles),
		WorkingDirectory: strings.Trim(strings.TrimSpace(req.WorkingDirectory), "/"),
		RateLimit:        req.RateLimit,
		// Tidied before validation so " Daily " from a hand-rolled client is
		// accepted, and so the persisted value is exactly one of the accepted
		// strings — the stanza emits it verbatim.
		LogRotation: siteoperator.LogRotation{
			Enabled:   req.LogRotation.Enabled,
			KeepFiles: req.LogRotation.KeepFiles,
			Frequency: strings.ToLower(strings.TrimSpace(req.LogRotation.Frequency)),
		},
		BasicAuth: siteoperator.BasicAuth{
			Enabled:  req.BasicAuth.Enabled,
			Realm:    strings.TrimSpace(req.BasicAuth.Realm),
			Username: strings.TrimSpace(req.BasicAuth.Username),
		},
	}
	if req.BasicAuth.Enabled {
		switch {
		case req.BasicAuth.Password != "":
			hash, err := bcrypt.GenerateFromPassword([]byte(req.BasicAuth.Password), bcrypt.DefaultCost)
			if err != nil {
				return siteoperator.Settings{}, fmt.Errorf("hash basic-auth password: %w", err)
			}
			settings.BasicAuth.PasswordHash = string(hash)
		case current.BasicAuth.PasswordHash != "" && settings.BasicAuth.Username == current.BasicAuth.Username:
			// Re-saving with a blank password keeps the existing credential.
			settings.BasicAuth.PasswordHash = current.BasicAuth.PasswordHash
		default:
			return siteoperator.Settings{}, errors.New("a password is required to enable basic authentication")
		}
	}
	if err := siteoperator.ValidateSettings(settings); err != nil {
		return siteoperator.Settings{}, err
	}
	return settings, nil
}

// tidyIndexFiles trims each submitted filename and drops blanks, so a UI that
// posts an empty trailing row (or a value typed with stray spaces) is accepted
// rather than 422'd. An all-blank list collapses to nil, which is exactly the
// zero value the renderer treats as the baseline — the same forgiveness the
// subdirectory field already gets from Trim/TrimSpace. A list that happens to
// equal the baseline is stored verbatim: it renders identically either way, so
// there is nothing to normalise away.
func tidyIndexFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	tidied := make([]string, 0, len(files))
	for _, name := range files {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			tidied = append(tidied, trimmed)
		}
	}
	if len(tidied) == 0 {
		return nil
	}
	return tidied
}

// redactedSecret replaces credential material in an API response. It is not an
// empty string so a reader can tell "withheld" apart from "this file is empty".
const redactedSecret = "[redacted]"

// htpasswdSuffix identifies the one managed artifact whose body is a credential.
const htpasswdSuffix = ".htpasswd"

// redactPlanForAPI returns a copy of a stored plan with every trace of the
// basic-auth hash removed, for the plan endpoint.
//
// The stored plan deliberately keeps the hash: the node re-renders plan.Site and
// requires byte-exact artifact equality, so a plan stripped of the credential
// would fail validation and no site with basic auth could ever be activated.
// Redaction therefore belongs on the response path only, and this function must
// never write back into the value it was handed — hence the fresh slices.
//
// The hash reaches a response through four separate routes, and all four matter:
// the settings field, the rendered htpasswd artifact body, the before-snapshot
// of that file, and — after basic auth is switched off — the retired-path
// snapshot, which still carries the contents of the file about to be deleted.
// Digests go too: they are computed over the credential line, so publishing them
// would let a guessed hash be confirmed offline.
func redactPlanForAPI(plan siteoperator.Plan) siteoperator.Plan {
	plan.Site.Settings.BasicAuth.PasswordHash = ""
	artifacts := make([]siteoperator.Artifact, len(plan.Artifacts))
	for index, artifact := range plan.Artifacts {
		if strings.HasSuffix(artifact.Path, htpasswdSuffix) {
			artifact.Content = redactedSecret
		}
		artifacts[index] = artifact
	}
	plan.Artifacts = artifacts
	plan.Before = redactSnapshots(plan.Before)
	plan.RetiredBefore = redactSnapshots(plan.RetiredBefore)
	return plan
}

func redactSnapshots(snapshots []siteoperator.Snapshot) []siteoperator.Snapshot {
	redacted := make([]siteoperator.Snapshot, len(snapshots))
	for index, snapshot := range snapshots {
		if strings.HasSuffix(snapshot.Path, htpasswdSuffix) && snapshot.Exists {
			snapshot.Content = redactedSecret
			snapshot.Digest = redactedSecret
		}
		redacted[index] = snapshot
	}
	return redacted
}

// clampTLSDomains keeps only the certificate names that still have a serving
// hostname on the site. A certificate's stored SAN list is not rewritten when a
// domain is removed — the domains module clamps at teardown for exactly this
// reason — so a settings edit that re-planned from the raw list would hand the
// operator a TLS name no longer attached to the site, which Renderer.validate
// rejects ("TLS domain is not attached to the site") and which would mark the
// site failed for an edit that had nothing to do with routing.
func clampTLSDomains(primary string, routes []siteoperator.Route, tlsDomains []string) []string {
	allowed := map[string]struct{}{primary: {}}
	for _, route := range routes {
		if route.Kind != "redirect" {
			allowed[route.Hostname] = struct{}{}
		}
	}
	clamped := make([]string, 0, len(tlsDomains))
	for _, hostname := range tlsDomains {
		if _, ok := allowed[hostname]; ok {
			clamped = append(clamped, hostname)
		}
	}
	return clamped
}

func (m *Module) markFailed(ctx context.Context, siteID string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusFailed).
		Set("failure = ?", message).Set("updated_at = ?", m.now().UTC()).Where("id = ?", siteID).Exec(ctx)
}

func (m *Module) submitPlanMutation(w http.ResponseWriter, r *http.Request, kind string, status Status) {
	site, siteErr := m.Get(r.Context(), r.PathValue("id"))
	if siteErr != nil {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if kind == "site.activate" && site.Status != StatusPlanReady {
		httpapi.WriteError(w, http.StatusConflict, "site_not_ready", "Only a reviewed, current site plan can be activated.")
		return
	}
	if kind == "site.rollback" && site.Status != StatusActive {
		httpapi.WriteError(w, http.StatusConflict, "site_not_active", "Only an active site can be rolled back.")
		return
	}
	plan, _, err := m.Plan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_plan_not_found", "A signed site plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_plan_unavailable", "The site plan could not be loaded.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	title := "Activate " + plan.Site.PrimaryDomain
	if kind == "site.rollback" {
		title = "Roll back " + plan.Site.PrimaryDomain
	}
	job, err := m.jobs.SubmitTitled(r.Context(), kind, title, plan, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "The site operation could not be queued.")
		return
	}
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", status).Set("last_job_id = ?", job.ID).Set("updated_at = ?", now).Where("id = ?", plan.Site.ID).Exec(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_update_failed", "The queued operation could not be attached to the site.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}

func validateCreate(request CreateRequest) error {
	if !slugPattern.MatchString(request.Slug) {
		return errors.New("slug must start with a letter and contain 2-32 lowercase letters, numbers, or hyphens")
	}
	if request.DisplayName == "" || len(request.DisplayName) > 80 {
		return errors.New("display name must contain 1-80 characters")
	}
	if !domainPattern.MatchString(request.PrimaryDomain) || len(request.PrimaryDomain) > 253 {
		return errors.New("primary domain must be a valid fully-qualified ASCII hostname")
	}
	return nil
}

func (model siteModel) toSite() Site {
	failure := ""
	if model.Failure != nil {
		failure = *model.Failure
	}
	// A malformed column must never fail a read; it falls back to the baseline,
	// which is exactly what the renderer would emit for it anyway.
	settings, _ := decodeSettings(model.SettingsJSON)
	// The password hash is a stored secret; the API surface never exposes it.
	settings.BasicAuth.PasswordHash = ""
	return Site{
		ID: model.ID, Slug: model.Slug, DisplayName: model.DisplayName, PrimaryDomain: model.PrimaryDomain,
		PHPVersion: model.PHPVersion, UnixUser: model.UnixUser, RootPath: model.RootPath, SocketPath: model.SocketPath,
		Status: Status(model.Status), Settings: settings, DeploymentMode: deploymentMode(model.DeploymentMode),
		LastJobID: model.LastJobID, Failure: failure, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

// validDeploymentMode reports whether v is a mode the panel knows how to render.
// The empty string is deliberately rejected: it is only ever a stored default,
// never a value a caller may ask for.
func validDeploymentMode(v string) bool {
	return v == DeploymentModeStandard || v == DeploymentModeDeployer
}

// deploymentMode normalises a stored column to a mode the renderer understands.
// Rows written before the column existed, and any value a future downgrade left
// behind, read back as standard — which is what the renderer emits for them
// anyway, so no backfill is required.
func deploymentMode(stored string) string {
	if !validDeploymentMode(stored) {
		return DeploymentModeStandard
	}
	return stored
}

// settingsEditable reports whether the site is in a settled state that a settings
// change may act on. Mid-operation states are rejected with 409 so an edit cannot
// interleave with an in-flight plan, activation, rollback, or deletion.
func settingsEditable(status Status) bool {
	switch status {
	case StatusActive, StatusPlanReady, StatusDraft, StatusRolledBack, StatusFailed:
		return true
	default:
		return false
	}
}

func randomID() string { return "site_" + secureid.Hex(12) }
