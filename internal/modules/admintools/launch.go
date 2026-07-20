package admintools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
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
	if kind == admintooloperator.PGAdmin {
		// pgAdmin has one process-wide server catalog and passfile. Serialize its
		// rotation so exactly one Nexa session matches the active capability
		// header and catalog at a time.
		m.pgAdminLaunchMu.Lock()
		defer m.pgAdminLaunchMu.Unlock()
	}
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
	if kind == admintooloperator.PGAdmin {
		// Rotating the global pgAdmin catalog/passfile makes every previous
		// launch unsafe. Expire both unused launch tokens and established proxy
		// sessions before publishing the replacement session.
		if err := m.expireLaunchesAt(ctx, admintooloperator.PGAdmin, now); err != nil {
			return "", "", err
		}
	}
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

func (m *Module) expireLaunches(ctx context.Context, kind admintooloperator.Kind) error {
	return m.expireLaunchesAt(ctx, kind, m.now().UTC())
}

func (m *Module) expireLaunchesAt(ctx context.Context, kind admintooloperator.Kind, at time.Time) error {
	_, err := m.database.NewUpdate().Model((*launchModel)(nil)).
		Set("expires_at = ?", at).
		Set("session_expires_at = ?", at).
		Where("tool_kind = ?", string(kind)).Exec(ctx)
	return err
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
	token, path, err := m.CreateLaunch(r.Context(), kind, request, user, httpapi.RemoteAddress(r))
	if err != nil {
		writeError(w, 409, "admin_tool_launch_failed", err.Error())
		return
	}
	secure := httpapi.IsHTTPS(r)
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
	if kind == admintooloperator.PGAdmin {
		if _, err := admintooloperator.PGAdminSessionHeader(sessionToken); err != nil {
			writeError(w, 401, "admin_tool_session_invalid", "Admin tool session is invalid.")
			return
		}
	}
	tool, err := m.get(r.Context(), kind)
	if err != nil || tool.Status != string(StatusActive) {
		writeError(w, 503, "admin_tool_unavailable", "Admin tool is not active.")
		return
	}
	// pgAdmin restarts during launch bootstrap to reload its per-database server
	// catalog, and systemd reports the unit active the instant the container
	// forks — several seconds before gunicorn binds its port. Wait for the
	// loopback listener so this navigation does not race the boot and surface as
	// a 502. It returns on the first successful dial, so an already-running tool
	// pays only a single local connect.
	if err := waitForUpstreamReady(r.Context(), tool.Port, 22*time.Second); err != nil {
		writeError(w, 502, "admin_tool_upstream_failed", "Admin tool did not respond.")
		return
	}
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", tool.Port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	prefix := "/tools/" + string(kind)
	secure := httpapi.IsHTTPS(r)
	proxy.Director = func(request *http.Request) {
		original(request)
		request.Host = target.Host
		request.URL.Path = strings.TrimPrefix(request.URL.Path, prefix)
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		sanitizeAdminToolProxyRequest(request, kind, model.UpstreamCookieName)
		if kind == admintooloperator.PGAdmin {
			_, _ = setPGAdminSessionIdentity(request, sessionToken, model.PanelUser)
		}
		if kind == admintooloperator.PHPMyAdmin && model.UpstreamCookieName != nil {
			request.AddCookie(&http.Cookie{Name: *model.UpstreamCookieName, Value: sessionToken})
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		rewriteAdminToolProxyResponse(response, kind, prefix, secure)
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writeError(writer, 502, "admin_tool_upstream_failed", "Admin tool did not respond.")
	}
	proxy.ServeHTTP(w, r)
}

// sanitizeAdminToolProxyRequest keeps panel credentials and proxy identity
// metadata out of third-party tool containers while preserving tool-owned
// cookies. The phpMyAdmin sign-on cookie is always replaced by the server-side
// capability after this function returns.
func sanitizeAdminToolProxyRequest(request *http.Request, kind admintooloperator.Kind, upstreamCookieName *string) {
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "Forwarded", "X-Forwarded-For",
		"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-User", "X-Real-IP",
		"Remote-User", "X-Original-URL", "X-Rewrite-URL",
	} {
		request.Header.Del(name)
	}
	// ReverseProxy treats a present nil slice as an explicit request not to add
	// its automatic X-Forwarded-For value after Director returns.
	request.Header["X-Forwarded-For"] = nil
	for name := range request.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-nexa-") {
			request.Header.Del(name)
		}
	}
	upstreamName := ""
	if upstreamCookieName != nil {
		upstreamName = *upstreamCookieName
	}
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	request.Header.Del("Cookie2")
	for _, cookie := range cookies {
		if (upstreamName != "" && cookie.Name == upstreamName) || !allowedToolCookie(kind, cookie.Name) {
			continue
		}
		request.AddCookie(cookie)
	}
}

// rewriteAdminToolProxyResponse confines redirects and cookies to the tool
// prefix. It also removes response controls that an upstream could otherwise
// ask the outer Nginx proxy to interpret.
func rewriteAdminToolProxyResponse(response *http.Response, kind admintooloperator.Kind, prefix string, secure bool) {
	for _, name := range []string{"X-Accel-Redirect", "X-Accel-Expires", "X-Accel-Limit-Rate", "X-Sendfile"} {
		response.Header.Del(name)
	}
	for name := range response.Header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			response.Header.Del(name)
		}
	}
	response.Header.Set("Cache-Control", "no-store")
	response.Header.Set("Pragma", "no-cache")
	if location := response.Header.Get("Location"); location != "" {
		path, rawQuery, fragment := "/", "", ""
		if parsed, err := url.Parse(location); err == nil {
			path = parsed.Path
			rawQuery = parsed.RawQuery
			fragment = parsed.Fragment
		}
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			path = prefix + "/" + strings.TrimPrefix(path, "/")
		}
		response.Header.Set("Location", (&url.URL{Path: path, RawQuery: rawQuery, Fragment: fragment}).String())
	}
	cookies := response.Cookies()
	response.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		if !allowedToolCookie(kind, cookie.Name) {
			continue
		}
		cookie.Path = prefix + "/"
		cookie.Domain = ""
		cookie.HttpOnly = true
		cookie.Secure = secure
		cookie.SameSite = http.SameSiteStrictMode
		response.Header.Add("Set-Cookie", cookie.String())
	}
}

func allowedToolCookie(kind admintooloperator.Kind, name string) bool {
	lower := strings.ToLower(name)
	switch kind {
	case admintooloperator.PHPMyAdmin:
		return lower == "phpmyadmin" || strings.HasPrefix(lower, "pma")
	case admintooloperator.PGAdmin:
		return lower == "pga4_session"
	default:
		return false
	}
}

// setPGAdminSessionIdentity removes every browser-supplied capability-shaped
// header, then adds the single server-derived header name trusted by this
// pgAdmin launch. The raw session token remains server-side and never appears
// in a URL, header value, or access-log request line.
func setPGAdminSessionIdentity(request *http.Request, sessionToken, panelUser string) (string, error) {
	prefix := strings.ToLower(admintooloperator.PGAdminSessionHeaderPrefix)
	for name := range request.Header {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			request.Header.Del(name)
		}
	}
	header, err := admintooloperator.PGAdminSessionHeader(sessionToken)
	if err != nil {
		return "", err
	}
	request.Header.Set(header, panelUser)
	return header, nil
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
	secure := httpapi.IsHTTPS(r)
	path := "/tools/" + string(kind) + "/"
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sessionToken, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 900})
	http.SetCookie(w, &http.Cookie{Name: launchCookieName, Value: "", Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (m *Module) sessionByHash(ctx context.Context, hash string) (launchModel, error) {
	var model launchModel
	err := m.database.NewSelect().Model(&model).Where("session_token_hash = ?", hash).Scan(ctx)
	return model, err
}

// waitForUpstreamReady blocks until the tool's loopback port accepts a
// connection or the timeout elapses, returning immediately on the first
// successful dial. A refused connection on loopback returns instantly, so the
// poll cycles quickly while the container is still booting.
func waitForUpstreamReady(ctx context.Context, port int, timeout time.Duration) error {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	dialer := net.Dialer{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func secureToken() (string, error) {
	value := make([]byte, 32)
	rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
