package admintools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
)

const (
	launchCookieName  = "nexa_tool_launch"
	sessionCookieName = "nexa_tool_session"
)

type LaunchRequest struct {
	SourceEngine string `json:"sourceEngine"`
	DatabaseID   string `json:"databaseId"`
	AccountID    string `json:"accountId"`
}

func (m *Module) CreateLaunch(ctx context.Context, kind admintooloperator.Kind, request LaunchRequest, user identity.User, remoteAddress string) (string, string, error) {
	if m.cipher == nil || m.resolver == nil || m.audit == nil {
		return "", "", errors.New("admin tool launch gateway is unavailable")
	}
	if (kind == admintooloperator.PHPMyAdmin && request.SourceEngine != "mysql") || (kind == admintooloperator.PGAdmin && request.SourceEngine != "postgresql") {
		return "", "", errors.New("admin tool does not match the selected database engine")
	}
	tool, err := m.get(ctx, kind)
	if err != nil || tool.Status != string(StatusActive) {
		return "", "", errors.New("admin tool must be active before launch")
	}
	credential, err := m.resolver(ctx, request.SourceEngine, strings.TrimSpace(request.DatabaseID), strings.TrimSpace(request.AccountID))
	if err != nil {
		return "", "", err
	}
	defer clear(credential.Secret)
	launchToken, err := secureToken()
	if err != nil {
		return "", "", err
	}
	sessionToken, err := secureToken()
	if err != nil {
		return "", "", err
	}
	secretDigest := sha256.Sum256(credential.Secret)
	panelUser := user.Username
	if kind == admintooloperator.PGAdmin && !strings.Contains(panelUser, "@") {
		panelUser += "@nexa.example.com"
	}
	change := admintooloperator.Change{Action: admintooloperator.ActionLaunch, Tool: tool.toTool().Tool, Launch: &admintooloperator.Launch{SessionID: sessionToken, PanelUser: panelUser, DatabaseHost: credential.Host, DatabasePort: credential.Port, Database: credential.Database, Username: credential.Username, SecretSHA256: hex.EncodeToString(secretDigest[:])}}
	plan, err := m.operator.Plan(ctx, change)
	if err != nil {
		return "", "", err
	}
	observation, err := m.operator.Apply(ctx, admintooloperator.Execution{Plan: plan, Secret: string(credential.Secret)})
	if err != nil {
		return "", "", err
	}
	if !observation.Verified {
		return "", "", errors.New("admin tool session bootstrap was not verified")
	}
	if observation.UpstreamCookieValue != "" && observation.UpstreamCookieValue != sessionToken {
		return "", "", errors.New("admin tool returned mismatched session material")
	}
	id := randomID()
	ciphertext, err := m.cipher.Encrypt("admin-tool-session:"+id, []byte(sessionToken))
	if err != nil {
		return "", "", err
	}
	now := m.now().UTC()
	var upstream *string
	if observation.UpstreamCookieName != "" {
		value := observation.UpstreamCookieName
		upstream = &value
	}
	model := launchModel{ID: id, ActorUserID: user.ID, PanelUser: panelUser, ToolKind: string(kind), SourceEngine: request.SourceEngine, DatabaseID: request.DatabaseID, AccountID: request.AccountID, LaunchTokenHash: tokenHash(launchToken), SessionTokenHash: tokenHash(sessionToken), SessionCiphertext: ciphertext, UpstreamCookieName: upstream, ExpiresAt: now.Add(60 * time.Second), SessionExpiresAt: now.Add(15 * time.Minute), CreatedAt: now}
	if _, err := m.database.NewInsert().Model(&model).Exec(ctx); err != nil {
		return "", "", err
	}
	actor := user.ID
	if err := m.audit.Record(ctx, audit.Entry{ActorUserID: &actor, Action: "admin_tool.launch", Subject: "admin-tool:" + string(kind), RemoteAddress: remoteAddress, Metadata: map[string]any{"engine": request.SourceEngine, "databaseId": request.DatabaseID, "accountId": request.AccountID}}); err != nil {
		_, _ = m.database.NewDelete().Model((*launchModel)(nil)).Where("id = ?", id).Exec(context.WithoutCancel(ctx))
		return "", "", err
	}
	return launchToken, "/tools/" + string(kind) + "/", nil
}

func (m *Module) launchHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	var request LaunchRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	kind := admintooloperator.Kind(r.PathValue("kind"))
	token, path, err := m.CreateLaunch(r.Context(), kind, request, user, r.RemoteAddr)
	if err != nil {
		writeError(w, 409, "admin_tool_launch_failed", err.Error())
		return
	}
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: launchCookieName, Value: token, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 60})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]string{"url": path})
}

func (m *Module) proxyHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	kind := admintooloperator.Kind(r.PathValue("kind"))
	if kind != admintooloperator.PHPMyAdmin && kind != admintooloperator.PGAdmin {
		http.NotFound(w, r)
		return
	}
	model, sessionToken, exchanged, err := m.authorizeProxy(r.Context(), kind, user.ID, r)
	if err != nil {
		writeError(w, 401, "admin_tool_session_invalid", err.Error())
		return
	}
	if exchanged {
		m.setProxySession(w, r, kind, sessionToken)
	}
	tool, err := m.get(r.Context(), kind)
	if err != nil || tool.Status != string(StatusActive) {
		writeError(w, 503, "admin_tool_unavailable", "Admin tool is not active.")
		return
	}
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", tool.Port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	prefix := "/tools/" + string(kind)
	proxy.Director = func(request *http.Request) {
		original(request)
		request.URL.Path = strings.TrimPrefix(request.URL.Path, prefix)
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		request.Header.Del("X-Forwarded-User")
		request.Header.Del("Remote-User")
		if kind == admintooloperator.PGAdmin {
			request.Header.Set("X-Forwarded-User", model.PanelUser)
		}
		if kind == admintooloperator.PHPMyAdmin && model.UpstreamCookieName != nil {
			request.AddCookie(&http.Cookie{Name: *model.UpstreamCookieName, Value: sessionToken})
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if location := response.Header.Get("Location"); location != "" {
			if parsed, parseErr := url.Parse(location); parseErr == nil {
				if parsed.IsAbs() {
					parsed.Scheme = ""
					parsed.Host = ""
				}
				if !strings.HasPrefix(parsed.Path, prefix) {
					parsed.Path = prefix + "/" + strings.TrimPrefix(parsed.Path, "/")
				}
				response.Header.Set("Location", parsed.String())
			}
		}
		cookies := response.Cookies()
		if len(cookies) > 0 {
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				cookie.Path = prefix + "/"
				response.Header.Add("Set-Cookie", cookie.String())
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writeError(writer, 502, "admin_tool_upstream_failed", "Admin tool did not respond.")
	}
	proxy.ServeHTTP(w, r)
}

func (m *Module) authorizeProxy(ctx context.Context, kind admintooloperator.Kind, userID string, r *http.Request) (launchModel, string, bool, error) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		model, loadErr := m.sessionByHash(ctx, tokenHash(cookie.Value))
		if loadErr == nil && model.ActorUserID == userID && model.ToolKind == string(kind) && m.now().UTC().Before(model.SessionExpiresAt) {
			return model, cookie.Value, false, nil
		}
	}
	launchCookie, err := r.Cookie(launchCookieName)
	if err != nil {
		return launchModel{}, "", false, errors.New("admin tool launch has expired")
	}
	model := launchModel{}
	if err := m.database.NewSelect().Model(&model).Where("launch_token_hash = ?", tokenHash(launchCookie.Value)).Scan(ctx); err != nil || model.UsedAt != nil || model.ActorUserID != userID || model.ToolKind != string(kind) || !m.now().UTC().Before(model.ExpiresAt) {
		return launchModel{}, "", false, errors.New("admin tool launch is invalid or already used")
	}
	plaintext, err := m.cipher.Decrypt("admin-tool-session:"+model.ID, model.SessionCiphertext)
	if err != nil {
		return launchModel{}, "", false, err
	}
	sessionToken := string(plaintext)
	clear(plaintext)
	now := m.now().UTC()
	result, err := m.database.NewUpdate().Model((*launchModel)(nil)).Set("used_at = ?", now).Where("id = ?", model.ID).Where("used_at IS NULL").Exec(ctx)
	if err != nil {
		return launchModel{}, "", false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return launchModel{}, "", false, errors.New("admin tool launch is already used")
	}
	model.UsedAt = &now
	return model, sessionToken, true, nil
}

// setProxySession writes the exchange cookies after authorizeProxy has consumed a launch.
func (m *Module) setProxySession(w http.ResponseWriter, r *http.Request, kind admintooloperator.Kind, sessionToken string) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	path := "/tools/" + string(kind) + "/"
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sessionToken, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 900})
	http.SetCookie(w, &http.Cookie{Name: launchCookieName, Value: "", Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (m *Module) sessionByHash(ctx context.Context, hash string) (launchModel, error) {
	var model launchModel
	err := m.database.NewSelect().Model(&model).Where("session_token_hash = ?", hash).Scan(ctx)
	return model, err
}
func secureToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func tokenHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
