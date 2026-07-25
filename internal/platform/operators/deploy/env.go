package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

const (
	// sharedEnvName is the only file this surface will ever open. It is a fixed
	// name for the same reason the deploy key's is: nothing a caller sends
	// decides which file under the site root gets rewritten.
	sharedEnvName = ".env"
	// releaseRootName and sharedDirName mirror the layout the sites operator
	// creates in deployer mode ({root}/app/shared). They are restated rather
	// than imported because that package imports nothing from this one and the
	// dependency must not start now; the sites operator's prepareDeployerLayout
	// is the definition, and a change there has to be mirrored here.
	releaseRootName = "app"
	sharedDirName   = "shared"
	// webGroupName owns the file's group so PHP-FPM's workers can read it while
	// the site account owns it and everyone else is shut out (0640).
	webGroupName = "www-data"
	// sharedEnvMode is deliberately not 0600: the pool runs as the site user but
	// a release's own tooling may read the file as www-data, and 0640 is the
	// widest that still excludes every other account on the node.
	sharedEnvMode = os.FileMode(0o640)
	// MaxSharedEnvBytes bounds the document in both directions. It is exported
	// because the control panel refuses an oversized body in the request that
	// typed it, rather than letting the node discover it a round trip later.
	MaxSharedEnvBytes = 64 * 1024
)

// EnvRequest names the site whose shared .env is being read or written. It
// carries the same derived identity every other request in this package does,
// and nothing else: the content is a separate argument so no accidental log of
// a request value can carry it.
type EnvRequest struct {
	Slug     string `json:"slug"`
	UnixUser string `json:"unixUser"`
	RootPath string `json:"rootPath"`
}

func (r EnvRequest) validate() error {
	return validateIdentity(r.Slug, r.UnixUser, r.RootPath)
}

func (r EnvRequest) sharedEnvPath() string {
	return filepath.Join(r.RootPath, releaseRootName, sharedDirName, sharedEnvName)
}

// EnvDocument is the shared .env plus the two facts that identify it without
// reading it. Bytes and SHA256 are what an audit entry is allowed to carry:
// the content is a secret by construction — database passwords, API tokens —
// and it is never logged, never audited, and never put in a job payload.
type EnvDocument struct {
	Path       string    `json:"path"`
	Present    bool      `json:"present"`
	Content    string    `json:"content"`
	Bytes      int       `json:"bytes"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// envSystem is the privileged surface the shared-env methods drive. Like
// deployKeySystem it is asserted from the operator's SSHNodeSystem instead of
// being folded into it, so this surface can be faked on its own.
type envSystem interface {
	// ReadSharedEnvFile reports the document, whether it exists at all, and
	// when it last changed. A missing file is not an error: a deployer-mode
	// site that has never been given one is the normal starting state.
	ReadSharedEnvFile(ctx context.Context, rootPath string) (string, bool, time.Time, error)
	// WriteSharedEnvFile publishes the document owned nexa_<slug>:www-data and
	// mode 0640, atomically, and reports its new modification time.
	WriteSharedEnvFile(ctx context.Context, rootPath, unixUser, content string) (time.Time, error)
}

var _ envSystem = (*SSHHostSystem)(nil)

// ErrSharedEnvMissing is returned when the site has no release tree to hold a
// shared .env. It is exported so the control panel can tell "this site is not
// in deployer mode yet" from a genuine node failure and say so.
var ErrSharedEnvMissing = errors.New("this site has no shared release directory")

// ReadSharedEnv returns the site's shared .env. It runs synchronously, on the
// request goroutine, because the content is a secret and a job's request and
// result are both persisted.
func (o *SSHHostOperator) ReadSharedEnv(ctx context.Context, request EnvRequest) (EnvDocument, error) {
	if err := request.validate(); err != nil {
		return EnvDocument{}, err
	}
	system, err := o.envs()
	if err != nil {
		return EnvDocument{}, err
	}
	content, present, modified, err := system.ReadSharedEnvFile(ctx, request.RootPath)
	if err != nil {
		return EnvDocument{}, err
	}
	// A file the panel cannot show whole must not be shown truncated: saving
	// what the editor rendered would silently discard the rest of it.
	if len(content) > MaxSharedEnvBytes {
		return EnvDocument{}, fmt.Errorf("shared .env is larger than the %d bytes the panel will edit", MaxSharedEnvBytes)
	}
	return envDocument(request, content, present, modified), nil
}

// WriteSharedEnv replaces the site's shared .env. The document is validated
// here as well as in the control panel, because this operator is reachable from
// the agent socket and its own boundary is the one that has to hold.
func (o *SSHHostOperator) WriteSharedEnv(ctx context.Context, request EnvRequest, content string) (EnvDocument, error) {
	if err := request.validate(); err != nil {
		return EnvDocument{}, err
	}
	if err := ValidateSharedEnv(content); err != nil {
		return EnvDocument{}, err
	}
	system, err := o.envs()
	if err != nil {
		return EnvDocument{}, err
	}
	modified, err := system.WriteSharedEnvFile(ctx, request.RootPath, request.UnixUser, content)
	if err != nil {
		return EnvDocument{}, err
	}
	return envDocument(request, content, true, modified), nil
}

// ValidateSharedEnv is the whole content boundary: a size cap and a NUL
// refusal. It is exported so the control panel applies exactly this rule to the
// request body, and the node applies it again to what arrives.
//
// NUL is rejected rather than stripped because a `.env` parser splits on lines
// and a NUL is how a value hides the rest of a line from a human reader while
// still reaching the process environment.
func ValidateSharedEnv(content string) error {
	if len(content) > MaxSharedEnvBytes {
		return fmt.Errorf("shared .env must be at most %d bytes", MaxSharedEnvBytes)
	}
	if strings.ContainsRune(content, 0) {
		return errors.New("shared .env must not contain a NUL byte")
	}
	return nil
}

// SharedEnvDigest is the SHA-256 of a document, hex encoded. It is what an
// audit entry records instead of the content: two entries with the same digest
// changed nothing, and no entry can leak a credential.
func SharedEnvDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func envDocument(request EnvRequest, content string, present bool, modified time.Time) EnvDocument {
	return EnvDocument{
		Path:       request.sharedEnvPath(),
		Present:    present,
		Content:    content,
		Bytes:      len(content),
		SHA256:     SharedEnvDigest(content),
		ModifiedAt: modified.UTC(),
	}
}

// envs narrows the operator's node system to the shared-env surface, exactly as
// deployKeys() does for the deploy-key surface.
func (o *SSHHostOperator) envs() (envSystem, error) {
	system, ok := o.system.(envSystem)
	if !ok {
		return nil, errors.New("this node system does not implement the shared environment")
	}
	return system, nil
}

// ReadSharedEnvFile reads {root}/app/shared/.env through nested os.Roots, so a
// symlink planted anywhere in the release tree cannot make the panel display —
// or, on the write path, overwrite — a file outside the site root.
func (s *SSHHostSystem) ReadSharedEnvFile(_ context.Context, rootPath string) (string, bool, time.Time, error) {
	directory, err := openSharedDir(rootPath)
	if err != nil {
		return "", false, time.Time{}, err
	}
	defer directory.Close()
	info, err := directory.Lstat(sharedEnvName)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, time.Time{}, nil
	}
	if err != nil {
		return "", false, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return "", false, time.Time{}, errors.New("shared .env is not a managed file")
	}
	if info.Size() > MaxSharedEnvBytes {
		return "", false, time.Time{}, fmt.Errorf("shared .env is larger than the %d bytes the panel will edit", MaxSharedEnvBytes)
	}
	content, err := directory.ReadFile(sharedEnvName)
	if err != nil {
		return "", false, time.Time{}, err
	}
	return string(content), true, info.ModTime(), nil
}

func (s *SSHHostSystem) WriteSharedEnvFile(_ context.Context, rootPath, unixUser, content string) (time.Time, error) {
	uid, _, err := s.accountIDs(unixUser)
	if err != nil {
		return time.Time{}, err
	}
	gid, err := s.webGroupID()
	if err != nil {
		return time.Time{}, err
	}
	directory, err := openSharedDir(rootPath)
	if err != nil {
		return time.Time{}, err
	}
	defer directory.Close()
	if err := publishOwnedFile(directory, sharedEnvName, []byte(content), sharedEnvMode, uid, gid); err != nil {
		return time.Time{}, err
	}
	info, err := directory.Lstat(sharedEnvName)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func (s *SSHHostSystem) webGroupID() (int, error) {
	lookup := s.lookupGroup
	if lookup == nil {
		lookup = user.LookupGroup
	}
	group, err := lookup(webGroupName)
	if err != nil {
		return 0, fmt.Errorf("look up %s group: %w", webGroupName, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil || gid < 0 {
		return 0, fmt.Errorf("%s group returned an invalid GID", webGroupName)
	}
	return gid, nil
}

// openSharedDir descends {root} -> app -> shared one os.Root at a time. The
// tree only exists in deployer mode, so a missing component is reported as
// ErrSharedEnvMissing rather than as a node failure.
func openSharedDir(rootPath string) (*os.Root, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open site root: %w", err)
	}
	defer root.Close()
	app, err := root.OpenRoot(releaseRootName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSharedEnvMissing
		}
		return nil, fmt.Errorf("open release root: %w", err)
	}
	defer app.Close()
	shared, err := app.OpenRoot(sharedDirName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSharedEnvMissing
		}
		return nil, fmt.Errorf("open shared directory: %w", err)
	}
	return shared, nil
}

// envWriteRequest is the wire shape of a write. The content rides in the body
// of one synchronous agent call and is not persisted anywhere along the way.
type envWriteRequest struct {
	EnvRequest
	Content string `json:"content"`
}

func (c *UnixClient) ReadSharedEnv(ctx context.Context, request EnvRequest) (EnvDocument, error) {
	var document EnvDocument
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/env/read", request, &document)
	return document, restoreEnvSentinel(err)
}

func (c *UnixClient) WriteSharedEnv(ctx context.Context, request EnvRequest, content string) (EnvDocument, error) {
	var document EnvDocument
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/env/write", envWriteRequest{EnvRequest: request, Content: content}, &document)
	return document, restoreEnvSentinel(err)
}

// restoreEnvSentinel turns the agent's flattened code back into the sentinel
// the node raised, the same way restoreSentinel does for the SFTP conflict.
func restoreEnvSentinel(err error) error {
	var responseError *agentclient.ResponseError
	if errors.As(err, &responseError) && responseError.Code == "shared_env_missing" {
		return ErrSharedEnvMissing
	}
	return err
}
