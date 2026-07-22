package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

// bootstrapTokenHeader carries the first-run bootstrap secret. It is the primary
// gate that stops an unauthenticated REMOTE client from claiming the admin
// account the moment the panel is exposed on a public hostname.
const bootstrapTokenHeader = "X-Nexa-Bootstrap-Token"

// SetBootstrapTokenPath configures where the first-run bootstrap token lives. It
// must be called before EnsureBootstrapToken. An empty path disables the token
// gate (development), leaving loopback as the only proof of local access.
func (m *Module) SetBootstrapTokenPath(path string) {
	m.bootstrapMu.Lock()
	m.bootstrapTokenPath = path
	m.bootstrapMu.Unlock()
}

// EnsureBootstrapToken provisions the first-run bootstrap gate. While no
// administrator exists it generates a random token (once, reused across
// restarts) and writes it to an owner-only file so a local operator can read it
// out of band. Once an administrator exists the token file is removed and the
// in-memory secret cleared, so the bootstrap endpoint is permanently closed.
func (m *Module) EnsureBootstrapToken(ctx context.Context) error {
	m.bootstrapMu.Lock()
	path := m.bootstrapTokenPath
	m.bootstrapMu.Unlock()
	if path == "" {
		return nil
	}
	required, err := m.bootstrapRequired(ctx)
	if err != nil {
		return err
	}
	if !required {
		m.clearBootstrapToken()
		return nil
	}
	// Reuse an existing token across restarts so a token already handed to the
	// operator stays valid until setup completes.
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if token := strings.TrimSpace(string(existing)); token != "" {
			m.bootstrapMu.Lock()
			m.bootstrapToken = token
			m.bootstrapMu.Unlock()
			m.logger.Warn("first-run bootstrap token is active", "path", path)
			return nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate bootstrap token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create bootstrap token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write bootstrap token: %w", err)
	}
	m.bootstrapMu.Lock()
	m.bootstrapToken = token
	m.bootstrapMu.Unlock()
	m.logger.Warn("first-run bootstrap token written; supply it via the "+bootstrapTokenHeader+" header to complete setup", "path", path)
	return nil
}

// clearBootstrapToken closes the bootstrap window: the in-memory secret is
// wiped and the on-disk token removed so it can never be replayed.
func (m *Module) clearBootstrapToken() {
	m.bootstrapMu.Lock()
	m.bootstrapToken = ""
	path := m.bootstrapTokenPath
	m.bootstrapMu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
}

// bootstrapRequestAllowed decides whether an unauthenticated caller may claim
// the administrator account. A loopback caller is accepted as proof of local
// access; otherwise the caller must present the exact bootstrap token. When no
// token was provisioned (empty path in development) only loopback is accepted,
// so a public bind can never be claimed without the token.
func (m *Module) bootstrapRequestAllowed(r *http.Request, bodyToken string) bool {
	if httpapi.IsLoopback(r) {
		return true
	}
	m.bootstrapMu.Lock()
	expected := m.bootstrapToken
	m.bootstrapMu.Unlock()
	if expected == "" {
		return false
	}
	presented := strings.TrimSpace(r.Header.Get(bootstrapTokenHeader))
	if presented == "" {
		presented = strings.TrimSpace(bodyToken)
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}
