package admintools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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

const (
	// toolSessionIdleWindow is how long a tool session survives without proxy
	// traffic. It matches the container idle-stop timer so the panel session and
	// the tool behind it die together instead of the session dying first.
	toolSessionIdleWindow = 15 * time.Minute
	// toolSessionMaxLifetime caps the sliding window: however active a session
	// is, it is never usable more than this long after its launch.
	toolSessionMaxLifetime = 8 * time.Hour
	// toolSessionRenewAfter keeps the slide off the hot path — the deadline is
	// only rewritten once this much of the idle window has been consumed, so a
	// page load's asset burst costs a single database write at most.
	toolSessionRenewAfter = 5 * time.Minute
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
	if err != nil || !launchableStatus(Status(tool.Status)) {
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
	// PanelUser is retained for audit only. pgAdmin no longer derives its login
	// identity from it — every launch signs in as PGAdminLoginUser, the owner of
	// the imported server — so no tool-specific reshaping is needed here.
	panelUser := user.Username
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
	model := launchModel{ID: id, ActorUserID: user.ID, PanelUser: panelUser, ToolKind: string(kind), SourceEngine: request.SourceEngine, DatabaseID: request.DatabaseID, AccountID: request.AccountID, LaunchTokenHash: tokenHash(launchToken), SessionTokenHash: tokenHash(sessionToken), SessionCiphertext: ciphertext, UpstreamCookieName: upstream, ExpiresAt: now.Add(60 * time.Second), SessionExpiresAt: now.Add(toolSessionIdleWindow), CreatedAt: now}
	if _, err := m.database.NewInsert().Model(&model).Exec(ctx); err != nil {
		return "", "", err
	}
	actor := user.ID
	if err := m.audit.Record(ctx, audit.Entry{ActorUserID: &actor, Action: "admin_tool.launch", Subject: "admin-tool:" + string(kind), RemoteAddress: remoteAddress, Metadata: map[string]any{"engine": request.SourceEngine, "databaseId": request.DatabaseID, "accountId": request.AccountID}}); err != nil {
		_, _ = m.database.NewDelete().Model((*launchModel)(nil)).Where("id = ?", id).Exec(context.WithoutCancel(ctx))
		return "", "", err
	}
	// The launch bootstrap restarted the container, so a tool the database recorded
	// as idle is running again. Reflect that immediately rather than waiting for the
	// next Sync, keeping the list and the just-established session consistent. The
	// guard leaves any in-flight lifecycle status untouched.
	_, _ = m.database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusActive).Set("updated_at = ?", now).Where("kind = ?", kind).Where("status = ?", StatusIdle).Exec(context.WithoutCancel(ctx))
	return launchToken, "/tools/" + string(kind) + "/", nil
}

// launchableStatus reports whether a tool can start a session. An on-demand tool
// idled by its stop timer is still installed, and the launch bootstrap restarts
// its container, so idle is launchable exactly like active.
func launchableStatus(status Status) bool {
	return status == StatusActive || status == StatusIdle
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
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	var request LaunchRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	kind := admintooloperator.Kind(r.PathValue("kind"))
	token, path, err := m.CreateLaunch(r.Context(), kind, request, user, httpapi.RemoteAddress(r))
	if err != nil {
		httpapi.WriteError(w, 409, "admin_tool_launch_failed", err.Error())
		return
	}
	secure := httpapi.IsHTTPS(r)
	http.SetCookie(w, &http.Cookie{Name: launchCookieName, Value: token, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 60})
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 201, map[string]string{"url": path})
}

func (m *Module) proxyHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	kind := admintooloperator.Kind(r.PathValue("kind"))
	if kind != admintooloperator.PHPMyAdmin && kind != admintooloperator.PGAdmin {
		http.NotFound(w, r)
		return
	}
	model, sessionToken, issueCookie, err := m.authorizeProxy(r.Context(), kind, user.ID, r)
	if err != nil {
		httpapi.WriteError(w, 401, "admin_tool_session_invalid", err.Error())
		return
	}
	if issueCookie {
		m.setProxySession(w, r, kind, sessionToken, model.SessionExpiresAt)
	}
	if kind == admintooloperator.PHPMyAdmin && r.PathValue("path") == "nexa-signon-failed" {
		httpapi.WriteError(w, 502, "admin_tool_signon_failed", "phpMyAdmin could not connect with the selected database account.")
		return
	}
	if kind == admintooloperator.PGAdmin {
		if _, err := admintooloperator.PGAdminSessionHeader(sessionToken); err != nil {
			httpapi.WriteError(w, 401, "admin_tool_session_invalid", "Admin tool session is invalid.")
			return
		}
	}
	tool, err := m.get(r.Context(), kind)
	if err != nil || !launchableStatus(Status(tool.Status)) {
		httpapi.WriteError(w, 503, "admin_tool_unavailable", "Admin tool is not active.")
		return
	}
	// pgAdmin restarts during launch bootstrap to reload its per-database server
	// catalog, and systemd reports the unit active the instant the container
	// forks — several seconds before gunicorn binds its port. Wait for the
	// loopback listener so this navigation does not race the boot and surface as
	// a 502. It returns on the first successful dial, so an already-running tool
	// pays only a single local connect.
	if err := waitForUpstreamReady(r.Context(), tool.Port, 2*time.Second); err != nil {
		if kind == admintooloperator.PGAdmin && r.Context().Err() == nil {
			if planUpstreamRecovery(m.liveToolStatus(r.Context(), kind)) == recoveryStarting {
				// The unit is up but its worker has not bound yet — a cold boot after a
				// launch. Bridge it with the auto-refreshing "Starting" page.
				writeAdminToolStarting(w)
				return
			}
			// The container is down and will not restart on its own; the idle-stop
			// unit scrubbed its launch credentials, so a bare restart would be
			// useless. Surface the tool as idle, retire the now-dead session, and tell
			// the browser to relaunch instead of spinning on a dead upstream forever.
			persistCtx := context.WithoutCancel(r.Context())
			m.markToolIdle(persistCtx, kind)
			_ = m.expireLaunches(persistCtx, kind)
			writeAdminToolEnded(w, kind)
			return
		}
		httpapi.WriteError(w, 502, "admin_tool_upstream_failed", "Admin tool did not respond.")
		return
	}
	// Continued proxy traffic re-arms an on-demand container's idle-stop timer so a
	// tool in active use is not stopped on a countdown fixed at its launch. This
	// runs at the cadence of session renewal (issueCookie), keeping the container
	// and the panel session aligned so they expire together on real inactivity.
	m.rearmActiveToolTimer(context.WithoutCancel(r.Context()), kind, tool.OnDemand, issueCookie)
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", tool.Port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	prefix := "/tools/" + string(kind)
	secure := httpapi.IsHTTPS(r)
	proxy.Director = func(request *http.Request) {
		original(request)
		request.URL.Path = strings.TrimPrefix(request.URL.Path, prefix)
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		sanitizeAdminToolProxyRequest(request, kind, model.UpstreamCookieName)
		if kind == admintooloperator.PGAdmin {
			// HTML is rewritten as a compatibility fallback when pgAdmin emits
			// root-relative assets despite X-Script-Name. Force an uncompressed
			// upstream representation so that rewrite is deterministic.
			request.Header.Set("Accept-Encoding", "identity")
			setPGAdminProxyMetadata(request, prefix, secure)
			_, _ = setPGAdminSessionIdentity(request, sessionToken)
		}
		if kind == admintooloperator.PHPMyAdmin && model.UpstreamCookieName != nil {
			request.AddCookie(&http.Cookie{Name: *model.UpstreamCookieName, Value: sessionToken})
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		return rewriteAdminToolProxyResponse(response, kind, prefix, secure)
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		httpapi.WriteError(writer, 502, "admin_tool_upstream_failed", "Admin tool did not respond.")
	}
	// A tool asset response can be many megabytes and, because the panel vhost
	// proxies /tools with proxy_buffering off, streams at the browser's pace — so
	// the transfer can outlast the API server's global WriteTimeout and be severed
	// mid-body. Lift the per-connection write deadline before proxying.
	extendProxyWriteDeadline(w)
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
		"Remote-User", "X-Original-URL", "X-Rewrite-URL", "X-Scheme", "X-Script-Name",
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

// setPGAdminProxyMetadata tells pgAdmin that the gateway publishes it below a
// subpath. Without X-Script-Name, pgAdmin emits root-relative asset URLs which
// escape the tool proxy and resolve to the panel SPA. Values are server-owned:
// sanitizeAdminToolProxyRequest removes any browser-supplied versions first.
func setPGAdminProxyMetadata(request *http.Request, prefix string, secure bool) {
	request.Header.Set("X-Script-Name", prefix)
	scheme := "http"
	if secure {
		scheme = "https"
	}
	request.Header.Set("X-Scheme", scheme)
}

// rewriteAdminToolProxyResponse confines redirects and cookies to the tool
// prefix. It also removes response controls that an upstream could otherwise
// ask the outer Nginx proxy to interpret.
func rewriteAdminToolProxyResponse(response *http.Response, kind admintooloperator.Kind, prefix string, secure bool) error {
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
	if kind != admintooloperator.PGAdmin || response.Body == nil {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/html" {
		return nil
	}
	const maxPGAdminHTMLBytes = 4 * 1024 * 1024
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxPGAdminHTMLBytes+1))
	_ = response.Body.Close()
	if err != nil {
		return fmt.Errorf("read pgAdmin HTML: %w", err)
	}
	if len(payload) > maxPGAdminHTMLBytes {
		return errors.New("pgAdmin HTML exceeded the rewrite limit")
	}
	rewritten := prefixPGAdminRootURLs(string(payload), prefix)
	response.Body = io.NopCloser(strings.NewReader(rewritten))
	response.ContentLength = int64(len(rewritten))
	response.Header.Set("Content-Length", fmt.Sprint(len(rewritten)))
	response.Header.Del("ETag")
	response.Header.Del("Content-MD5")
	response.Header.Del("Accept-Ranges")
	return nil
}

var pgAdminRootAttribute = regexp.MustCompile(`(?i)\b(?:href|src|action)\s*=\s*["']/[^"']*`)

func prefixPGAdminRootURLs(page, prefix string) string {
	return pgAdminRootAttribute.ReplaceAllStringFunc(page, func(attribute string) string {
		quote := strings.IndexAny(attribute, `"'`)
		if quote < 0 || quote+1 >= len(attribute) {
			return attribute
		}
		path := attribute[quote+1:]
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return attribute
		}
		return attribute[:quote+1] + prefix + path
	})
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
// pgAdmin launch. Its value is the fixed owner identity (PGAdminLoginUser) so the
// browser signs in as the same user that owns the imported server and lands on it
// already connected; using a per-panel-user name would make that server a foreign
// shared server pgAdmin cannot auto-connect. The raw session token remains
// server-side and never appears in a URL, header value, or access-log line.
func setPGAdminSessionIdentity(request *http.Request, sessionToken string) (string, error) {
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
	request.Header.Set(header, admintooloperator.PGAdminLoginUser)
	return header, nil
}

func (m *Module) authorizeProxy(ctx context.Context, kind admintooloperator.Kind, userID string, r *http.Request) (launchModel, string, bool, error) {
	launchCookie, launchErr := r.Cookie(launchCookieName)
	if launchErr == nil {
		model := launchModel{}
		if err := m.database.NewSelect().Model(&model).Where("launch_token_hash = ?", tokenHash(launchCookie.Value)).Scan(ctx); err == nil && model.UsedAt == nil && model.ActorUserID == userID && model.ToolKind == string(kind) && m.now().UTC().Before(model.ExpiresAt) {
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
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		model, loadErr := m.sessionByHash(ctx, tokenHash(cookie.Value))
		now := m.now().UTC()
		if loadErr == nil && model.ActorUserID == userID && model.ToolKind == string(kind) && now.Before(model.SessionExpiresAt) {
			renewed, err := m.renewSession(ctx, &model, now)
			if err != nil {
				return launchModel{}, "", false, err
			}
			return model, cookie.Value, renewed, nil
		}
	}
	if launchErr != nil {
		return launchModel{}, "", false, errors.New("admin tool launch has expired")
	}
	return launchModel{}, "", false, errors.New("admin tool launch is invalid or already used")
}

// renewSession slides the tool session deadline forward on proxy activity, the
// panel-side mirror of the container idle timer: a tool in continuous use must
// not die on a fixed wall-clock deadline. The slide is bounded by
// toolSessionMaxLifetime and is only persisted once toolSessionRenewAfter of the
// window has elapsed, so a burst of asset requests does not write per request.
func (m *Module) renewSession(ctx context.Context, model *launchModel, now time.Time) (bool, error) {
	deadline := now.Add(toolSessionIdleWindow)
	if limit := model.CreatedAt.UTC().Add(toolSessionMaxLifetime); deadline.After(limit) {
		deadline = limit
	}
	if !deadline.After(model.SessionExpiresAt) || model.SessionExpiresAt.Sub(now) > toolSessionIdleWindow-toolSessionRenewAfter {
		return false, nil
	}
	// The deadline predicate keeps an expiry decided elsewhere — a relaunch
	// rotating the pgAdmin catalog, a tool lifecycle change — from being undone
	// by an asset request that was already in flight.
	result, err := m.database.NewUpdate().Model((*launchModel)(nil)).
		Set("session_expires_at = ?", deadline).
		Where("id = ?", model.ID).Where("session_expires_at > ?", now).Exec(ctx)
	if err != nil {
		return false, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return false, errors.New("admin tool session has expired")
	}
	model.SessionExpiresAt = deadline
	return true, nil
}

// setProxySession writes the session cookie after authorizeProxy has consumed a
// launch or slid the deadline forward. Its lifetime tracks the server-side
// deadline so the browser never drops a cookie the gateway still honours.
func (m *Module) setProxySession(w http.ResponseWriter, r *http.Request, kind admintooloperator.Kind, sessionToken string, expiresAt time.Time) {
	secure := httpapi.IsHTTPS(r)
	path := "/tools/" + string(kind) + "/"
	maxAge := max(int(expiresAt.Sub(m.now().UTC()).Seconds()), 1)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sessionToken, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
	http.SetCookie(w, &http.Cookie{Name: launchCookieName, Value: "", Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (m *Module) sessionByHash(ctx context.Context, hash string) (launchModel, error) {
	var model launchModel
	err := m.database.NewSelect().Model(&model).Where("session_token_hash = ?", hash).Scan(ctx)
	return model, err
}

// recoveryAction is how the proxy answers when a tool's upstream is not yet
// reachable: bridge a cold boot, or tell the user their session ended.
type recoveryAction int

const (
	recoveryStarting recoveryAction = iota // the unit is up/booting — retry behind the Starting page
	recoveryEnded                          // the unit is down — a fresh launch is required
)

// planUpstreamRecovery decides the answer from the tool's live systemd status. A
// status systemd still reports as up means the container is mid-boot after a
// launch and will bind its port shortly, so the caller retries. Any other state
// — including an empty status when the live probe itself failed — means the
// idle-stop timer (or a crash) took the container down; it will not return on its
// own and its launch credentials were scrubbed, so only a fresh launch recovers.
func planUpstreamRecovery(liveStatus string) recoveryAction {
	switch liveStatus {
	case "active", "activating", "reloading":
		return recoveryStarting
	default:
		return recoveryEnded
	}
}

// liveToolStatus returns the tool's current systemd status straight from the
// agent, bypassing the panel database whose record can lag behind an idle-stop.
// An empty string on any failure steers recovery toward the safe "relaunch"
// answer rather than an endless retry.
func (m *Module) liveToolStatus(ctx context.Context, kind admintooloperator.Kind) string {
	tools, err := m.operator.Discover(ctx)
	if err != nil {
		return ""
	}
	for _, tool := range tools {
		if tool.Kind == kind {
			return tool.Status
		}
	}
	return ""
}

// markToolIdle records that an on-demand tool the proxy just found down was
// auto-stopped, so the Applications list reflects it without waiting for the next
// Sync. The guard keeps in-flight lifecycle statuses and a recorded uninstall
// untouched.
func (m *Module) markToolIdle(ctx context.Context, kind admintooloperator.Kind) {
	_, _ = m.database.NewUpdate().Model((*toolModel)(nil)).
		Set("status = ?", StatusIdle).Set("updated_at = ?", m.now().UTC()).
		Where("kind = ?", kind).Where("status IN (?, ?)", StatusActive, StatusIdle).Exec(ctx)
}

// rearmActiveToolTimer re-arms an on-demand tool's idle-stop timer when live
// proxy activity has slid its session forward, so continued use keeps the
// container alive. Best-effort: a miss only risks an early idle-stop, which the
// recovery path handles gracefully.
func (m *Module) rearmActiveToolTimer(ctx context.Context, kind admintooloperator.Kind, onDemand, slid bool) {
	if !onDemand || !slid {
		return
	}
	_ = m.operator.KeepAlive(ctx, kind)
}

// writeAdminToolEnded replaces the infinite "Starting" spinner for a tool whose
// container was stopped out from under a still-valid session. It states plainly
// that the session ended and links back to the databases page, where the per-row
// launch lives, so the user relaunches instead of watching a dead upstream.
func writeAdminToolEnded(w http.ResponseWriter, kind admintooloperator.Kind) {
	name := displayName(kind)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusGone)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>`+name+` session ended</title><style>body{background:#f7f8fa;color:#172033;font:16px system-ui,sans-serif;display:grid;min-height:100vh;margin:0;place-items:center}.card{background:white;border:1px solid #dfe3ea;border-radius:14px;box-shadow:0 12px 35px #17203314;max-width:32rem;padding:2rem;text-align:center}h1{font-size:1.35rem;margin:.25rem 0 .75rem}p{color:#596579;line-height:1.55;margin:0 0 1.25rem}a{display:inline-block;background:#2f6bff;color:white;text-decoration:none;border-radius:9px;padding:.6rem 1.1rem;font-weight:600}</style><body><main class="card"><h1>`+name+` session ended</h1><p>This isolated database client was stopped after a period of inactivity. Relaunch it from the database it belongs to.</p><a href="/databases">Back to databases</a></main></body></html>`)
}

func writeAdminToolStarting(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Refresh", "3")
	w.Header().Set("Retry-After", "3")
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta http-equiv="refresh" content="3"><meta name="viewport" content="width=device-width"><title>Starting pgAdmin</title><style>body{background:#f7f8fa;color:#172033;font:16px system-ui,sans-serif;display:grid;min-height:100vh;margin:0;place-items:center}.card{background:white;border:1px solid #dfe3ea;border-radius:14px;box-shadow:0 12px 35px #17203314;max-width:32rem;padding:2rem;text-align:center}h1{font-size:1.35rem;margin:.25rem 0 .75rem}p{color:#596579;line-height:1.55;margin:0}</style><body><main class="card"><h1>Starting pgAdmin…</h1><p>The isolated database client is warming up. This page will retry automatically.</p></main></body></html>`)
}

// waitForUpstreamReady waits for a complete HTTP response, not merely an open
// TCP listener. Podman's port-forwarder can accept connections well before
// pgAdmin's Gunicorn worker is able to serve a request.
//
// Redirects are never followed: any HTTP status — including a 3xx — proves the
// upstream is serving, which is all readiness needs to establish. phpMyAdmin in
// particular answers a cookieless GET / with a 302 to its SignonURL that loops
// for any client without a signon session, so following redirects would turn a
// healthy upstream into a probe failure (and a spurious 502 at launch).
func waitForUpstreamReady(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Timeout: time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/", port)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr != nil {
				return lastErr
			}
			return context.DeadlineExceeded
		}
		attemptTimeout := min(remaining, time.Second)
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		request, _ := http.NewRequestWithContext(attemptCtx, http.MethodGet, endpoint, nil)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			cancel()
			return nil
		}
		cancel()
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pause := min(250*time.Millisecond, time.Until(deadline))
		if pause <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

// proxyWriteWindow bounds a single admin-tool proxy response. pgAdmin ships
// ~12 MB of vendor JavaScript, and the panel vhost proxies /tools with
// proxy_buffering off, so the browser's own pace backpressures all the way to
// this connection's socket writes — a real asset load routinely outlasts the
// API server's 30s WriteTimeout. The window is generous enough for a slow client
// pulling those bundles yet still bounds a wedged write, rather than clearing
// the deadline outright.
const proxyWriteWindow = 10 * time.Minute

// extendProxyWriteDeadline lifts the API server's global WriteTimeout for a
// single admin-tool proxy response. Without it a multi-megabyte asset is severed
// mid-body once the 30s deadline elapses and the browser reports the truncated
// stream as ERR_HTTP2_PROTOCOL_ERROR on an otherwise-200 response. This mirrors
// the per-connection write deadline the job and log SSE streams already set
// (internal/platform/jobs/http.go).
func extendProxyWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(proxyWriteWindow))
}

// secureToken mints a 256-bit random session/launch token as hex. Hex is a
// deliberate choice over base64url: a tool session token becomes phpMyAdmin's
// SignonSession cookie, which phpMyAdmin passes straight to PHP's session_id().
// PHP only accepts [A-Za-z0-9,-] as a session id, so base64url's '_' made PHP
// discard the id, start an empty session, and bounce ~half of all launches to
// nexa-signon-failed at random. Hex stays safely inside that set (and inside a
// cookie value, which forbids the ','), for both admin tools.
func secureToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func tokenHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
