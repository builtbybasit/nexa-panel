package sitefs

import (
	"errors"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/naming"
)

// Scope identifies one managed site's confinement root. It is sent by the
// control panel and independently re-validated by the agent before any
// filesystem access.
type Scope struct {
	SiteID   string `json:"siteId"`
	Slug     string `json:"slug"`
	RootPath string `json:"rootPath"`
	UnixUser string `json:"unixUser"`
}

func Validate(scope Scope, siteRoot string) error {
	if scope.SiteID == "" {
		return errors.New("site scope requires a site ID")
	}
	if !naming.ValidSiteSlug(scope.Slug) {
		return errors.New("site scope slug is not a managed site slug")
	}
	if scope.RootPath != filepath.Join(siteRoot, scope.Slug) {
		return errors.New("site scope root path does not match the managed site layout")
	}
	if scope.UnixUser != naming.SiteUnixUser(scope.Slug) {
		return errors.New("site scope unix user does not match the managed site layout")
	}
	return nil
}

// OpenRoot confines all subsequent path resolution beneath the site root at
// the kernel level. The caller must Close the returned root.
func OpenRoot(scope Scope) (*os.Root, error) {
	return os.OpenRoot(scope.RootPath)
}

// CleanRelative normalizes a client-supplied path into a slash-separated
// path relative to the site root, rejecting anything that could escape it.
func CleanRelative(raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("path must not contain NUL bytes")
	}
	if raw == "" || raw == "/" {
		return ".", nil
	}
	if strings.HasPrefix(raw, "/") {
		return "", errors.New("path must be relative to the site root")
	}
	cleaned := path.Clean(raw)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must stay within the site root")
	}
	return cleaned, nil
}

type Ownership interface {
	Chown(path string, unixUser string) error
}

// HostOwnership assigns entries to the site account with the shared
// web-server group, matching the provisioned site directory layout.
type HostOwnership struct{}

func (HostOwnership) Chown(path string, unixUser string) error {
	account, err := user.Lookup(unixUser)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if group, groupErr := user.LookupGroup("www-data"); groupErr == nil {
		if parsed, parseErr := strconv.Atoi(group.Gid); parseErr == nil {
			gid = parsed
		}
	}
	return os.Lchown(path, uid, gid)
}
