package php

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	phpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/php"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

// registerHTTP binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) registerHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listPhpVersions":     m.versionsHTTP,
		"listPhpExtensions":   m.extensionsHTTP,
		"listPhpSettings":     m.settingsHTTP,
		"changePhpExtension":  m.changeExtensionHTTP,
		"savePhpSettings":     m.saveSettingsHTTP,
		"listSitePhpSettings": m.siteSettingsHTTP,
		"saveSitePhpSettings": m.saveSiteSettingsHTTP,
	})
}

func (m *Module) versionsHTTP(w http.ResponseWriter, r *http.Request) {
	versions, err := m.Versions(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "php_versions_unavailable", "Installed PHP versions could not be discovered.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": versions})
}

func (m *Module) extensionsHTTP(w http.ResponseWriter, r *http.Request) {
	extensions, err := m.Extensions(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_extensions_unavailable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": extensions})
}

func (m *Module) settingsHTTP(w http.ResponseWriter, r *http.Request) {
	directives, err := m.Settings(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_settings_unavailable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": directives})
}

type changeExtensionRequest struct {
	Version   string `json:"version"`
	Extension string `json:"extension"`
	Action    string `json:"action"`
}

func (m *Module) changeExtensionHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[changeExtensionRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	action := phpoperator.ActionInstallExtension
	if request.Action == "remove" {
		action = phpoperator.ActionRemoveExtension
	} else if request.Action != "install" {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_extension_action_invalid", "Extension action must be install or remove.")
		return
	}
	job, err := m.ChangeExtension(r.Context(), request.Version, request.Extension, action, actor)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_extension_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

type saveSettingsRequest struct {
	Version string            `json:"version"`
	Set     map[string]string `json:"set"`
	Reset   []string          `json:"reset"`
}

func (m *Module) saveSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[saveSettingsRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.SaveSettings(r.Context(), request.Version, request.Set, request.Reset, actor)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_settings_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// resolveSite loads the site, hides it from unauthorized users, and requires it
// to be active. Inaccessible sites read as 404 so existence never leaks — the
// same contract the files module uses.
func (m *Module) resolveSite(w http.ResponseWriter, r *http.Request) (sites.Site, identity.User, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, identity.User{}, false
	}
	site, err := m.sites.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, identity.User{}, false
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, identity.User{}, false
	}
	accessible, err := m.access.SiteAccessible(r.Context(), user, site.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, identity.User{}, false
	}
	if !accessible {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, identity.User{}, false
	}
	if site.Status != sites.StatusActive {
		httpapi.WriteError(w, http.StatusConflict, "site_not_active", "PHP settings are only available while the site is active.")
		return sites.Site{}, identity.User{}, false
	}
	return site, user, true
}

func (m *Module) siteSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	directives, err := m.SiteSettings(r.Context(), site)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_site_settings_unavailable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": directives, "version": site.PHPVersion})
}

func (m *Module) saveSiteSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	request, decodeErr := webhandler.Decode[saveSettingsRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, _ := webhandler.ActorID(r)
	job, err := m.SaveSiteSettings(r.Context(), site, request.Set, request.Reset, actor)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "php_site_settings_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}
