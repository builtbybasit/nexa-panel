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
)

func (m *Module) registerHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/php/versions", "applications.read", http.HandlerFunc(m.versionsHTTP)},
		{"GET /api/v1/php/extensions", "applications.read", http.HandlerFunc(m.extensionsHTTP)},
		{"GET /api/v1/php/settings", "applications.read", http.HandlerFunc(m.settingsHTTP)},
		{"POST /api/v1/php/extensions", "applications.write", http.HandlerFunc(m.changeExtensionHTTP)},
		{"PUT /api/v1/php/settings", "applications.write", http.HandlerFunc(m.saveSettingsHTTP)},
		{"GET /api/v1/php/sites/{id}/settings", "applications.read", http.HandlerFunc(m.siteSettingsHTTP)},
		{"PUT /api/v1/php/sites/{id}/settings", "applications.write", http.HandlerFunc(m.saveSiteSettingsHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) versionsHTTP(w http.ResponseWriter, r *http.Request) {
	versions, err := m.Versions(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "php_versions_unavailable", "Installed PHP versions could not be discovered.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": versions})
}

func (m *Module) extensionsHTTP(w http.ResponseWriter, r *http.Request) {
	extensions, err := m.Extensions(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_extensions_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": extensions})
}

func (m *Module) settingsHTTP(w http.ResponseWriter, r *http.Request) {
	directives, err := m.Settings(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_settings_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": directives})
}

type changeExtensionRequest struct {
	Version   string `json:"version"`
	Extension string `json:"extension"`
	Action    string `json:"action"`
}

func (m *Module) changeExtensionHTTP(w http.ResponseWriter, r *http.Request) {
	var request changeExtensionRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	action := phpoperator.ActionInstallExtension
	if request.Action == "remove" {
		action = phpoperator.ActionRemoveExtension
	} else if request.Action != "install" {
		writeError(w, http.StatusUnprocessableEntity, "php_extension_action_invalid", "Extension action must be install or remove.")
		return
	}
	job, err := m.ChangeExtension(r.Context(), request.Version, request.Extension, action, actor)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_extension_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

type saveSettingsRequest struct {
	Version string            `json:"version"`
	Set     map[string]string `json:"set"`
	Reset   []string          `json:"reset"`
}

func (m *Module) saveSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	var request saveSettingsRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.SaveSettings(r.Context(), request.Version, request.Set, request.Reset, actor)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_settings_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// resolveSite loads the site, hides it from unauthorized users, and requires it
// to be active. Inaccessible sites read as 404 so existence never leaks — the
// same contract the files module uses.
func (m *Module) resolveSite(w http.ResponseWriter, r *http.Request) (sites.Site, identity.User, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, identity.User{}, false
	}
	site, err := m.sites.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, identity.User{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, identity.User{}, false
	}
	accessible, err := m.access.SiteAccessible(r.Context(), user, site.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, identity.User{}, false
	}
	if !accessible {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, identity.User{}, false
	}
	if site.Status != sites.StatusActive {
		writeError(w, http.StatusConflict, "site_not_active", "PHP settings are only available while the site is active.")
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
		writeError(w, http.StatusUnprocessableEntity, "php_site_settings_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": directives, "version": site.PHPVersion})
}

func (m *Module) saveSiteSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	var request saveSettingsRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, _ := actorID(r)
	job, err := m.SaveSiteSettings(r.Context(), site, request.Set, request.Reset, actor)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_site_settings_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func actorID(r *http.Request) (*string, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return nil, false
	}
	return &user.ID, true
}

var (
	decodeJSON = httpapi.DecodeJSON
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)
