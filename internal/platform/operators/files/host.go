package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

const (
	listMaxEntries = 5000
	// listMaxScan bounds what a single listing pulls off disk. The sort that
	// picks the displayed page needs more than one page in hand, but not a
	// whole directory: a site with a million-file upload folder would otherwise
	// read every entry into memory to render 5000 of them.
	listMaxScan       = 50_000
	readMaxBytes      = 1 << 20
	writeMaxBytes     = 1 << 20
	uploadMaxBytes    = int64(2) << 30
	copyMaxEntries    = 10_000
	copyMaxBytes      = int64(1) << 30
	archiveMaxEntries = 10_000
	archiveMaxBytes   = int64(1) << 30
	extractMaxEntries = 10_000
	extractMaxBytes   = int64(1) << 30
	sizeMaxEntries    = 1_000_000
	uploadStaleAfter  = 24 * time.Hour

	fileMode      = 0o640
	directoryMode = 0o750
)

var writeZones = map[string]struct{}{"public": {}, "private": {}, "tmp": {}, "backups": {}}

type HostOperator struct {
	SiteRoot string
	Owner    sitefs.Ownership
	now      func() time.Time
}

var _ Operator = (*HostOperator)(nil)

func NewHostOperator(siteRoot string, owner sitefs.Ownership) (*HostOperator, error) {
	if owner == nil {
		return nil, errors.New("files ownership manager is required")
	}
	if siteRoot == "" {
		siteRoot = "/srv/nexa/sites"
	}
	if !filepath.IsAbs(siteRoot) {
		return nil, errors.New("files site root must be absolute")
	}
	return &HostOperator{SiteRoot: filepath.Clean(siteRoot), Owner: owner, now: time.Now}, nil
}

func (o *HostOperator) List(_ context.Context, scope sitefs.Scope, rawPath string) (Listing, error) {
	root, relative, err := o.resolve(scope, rawPath)
	if err != nil {
		return Listing{}, err
	}
	defer root.Close()
	directory, err := root.Open(relative)
	if err != nil {
		return Listing{}, pathError(err, "The requested directory does not exist.")
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return Listing{}, err
	}
	if !info.IsDir() {
		return Listing{}, invalid("The requested path is not a directory.")
	}
	children, unread, err := readDirLimited(directory, listMaxScan)
	if err != nil {
		return Listing{}, err
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].IsDir() != children[j].IsDir() {
			return children[i].IsDir()
		}
		return children[i].Name() < children[j].Name()
	})
	listing := Listing{Entries: make([]Entry, 0, min(len(children), listMaxEntries)), Truncated: unread}
	for _, child := range children {
		if len(listing.Entries) == listMaxEntries {
			listing.Truncated = true
			break
		}
		childInfo, err := child.Info()
		if err != nil {
			continue
		}
		listing.Entries = append(listing.Entries, entryFor(child.Name(), childInfo))
	}
	return listing, nil
}

// readDirLimited reads at most limit entries from an open directory and reports
// whether the directory held more. Every caller here works to a cap, and
// ReadDir(-1) would materialise the whole directory before any cap could apply.
func readDirLimited(directory *os.File, limit int) ([]fs.DirEntry, bool, error) {
	// An empty or fully-consumed directory answers a bounded read with io.EOF;
	// that is the end of the listing, not a failure.
	children, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(children) > limit {
		return children[:limit], true, nil
	}
	return children, false, nil
}

func (o *HostOperator) Stat(_ context.Context, scope sitefs.Scope, rawPath string) (Entry, error) {
	root, relative, err := o.resolve(scope, rawPath)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil {
		return Entry{}, pathError(err, "The requested path does not exist.")
	}
	return entryFor(path.Base(relative), info), nil
}

func (o *HostOperator) Read(_ context.Context, scope sitefs.Scope, rawPath string) (ReadResult, error) {
	root, relative, err := o.resolve(scope, rawPath)
	if err != nil {
		return ReadResult{}, err
	}
	defer root.Close()
	file, _, err := openRegular(root, relative)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	digest := sha256.New()
	preview := make([]byte, 0, 64*1024)
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			digest.Write(buffer[:read])
			size += int64(read)
			if len(preview) < readMaxBytes {
				preview = append(preview, buffer[:min(read, readMaxBytes-len(preview))]...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ReadResult{}, readErr
		}
	}
	result := ReadResult{ETag: hex.EncodeToString(digest.Sum(nil)), Size: size, Truncated: size > readMaxBytes}
	result.Binary = bytes.IndexByte(preview[:min(len(preview), 8192)], 0) >= 0
	if !result.Binary && !result.Truncated {
		result.Content = string(preview)
	}
	return result, nil
}

func (o *HostOperator) Write(_ context.Context, scope sitefs.Scope, rawPath, content, expectedETag string) (WriteResult, error) {
	if len(content) > writeMaxBytes {
		return WriteResult{}, invalid("File content must be 1 MiB or smaller; use an upload for larger files.")
	}
	root, relative, err := o.resolveMutable(scope, rawPath)
	if err != nil {
		return WriteResult{}, err
	}
	defer root.Close()
	existing, err := root.Lstat(relative)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expectedETag != "" {
			return WriteResult{}, etagConflict("The file no longer exists; refresh before saving.")
		}
	case err != nil:
		return WriteResult{}, pathError(err, "The requested path does not exist.")
	default:
		if err := requireRegular(existing, "edited"); err != nil {
			return WriteResult{}, err
		}
		if expectedETag == "" {
			return WriteResult{}, existsConflict("A file already exists at this path; supply its current etag to overwrite it.")
		}
		current, err := fileETag(root, relative)
		if err != nil {
			return WriteResult{}, err
		}
		if current != expectedETag {
			return WriteResult{}, etagConflict("The file changed since it was loaded; reload it before saving.")
		}
	}
	if err := o.stageFile(scope, root, relative, strings.NewReader(content), fileMode); err != nil {
		return WriteResult{}, err
	}
	sum := sha256.Sum256([]byte(content))
	return WriteResult{ETag: hex.EncodeToString(sum[:])}, nil
}

func (o *HostOperator) Mkdir(_ context.Context, scope sitefs.Scope, rawPath string) (Entry, error) {
	root, relative, err := o.resolveMutable(scope, rawPath)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	if err := o.makeDirectories(scope, root, relative); err != nil {
		return Entry{}, err
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return Entry{}, pathError(err, "The requested path does not exist.")
	}
	return entryFor(path.Base(relative), info), nil
}

func (o *HostOperator) Move(_ context.Context, scope sitefs.Scope, rawFrom, rawTo string, overwrite bool) (Entry, error) {
	root, from, err := o.resolveMutable(scope, rawFrom)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	to, err := relativeMutable(rawTo)
	if err != nil {
		return Entry{}, err
	}
	source, err := root.Lstat(from)
	if err != nil {
		return Entry{}, pathError(err, "The source path does not exist.")
	}
	if source.IsDir() && (to == from || strings.HasPrefix(to, from+"/")) {
		return Entry{}, invalid("A directory cannot be moved into itself.")
	}
	target, err := root.Lstat(to)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Entry{}, pathError(err, "The target path is not reachable.")
	}
	if err == nil {
		if !overwrite {
			return Entry{}, existsConflict("An entry already exists at the target path.")
		}
		if target.IsDir() || source.IsDir() {
			return Entry{}, invalid("Directories cannot be replaced by a move; delete the target first.")
		}
	}
	if err := root.Rename(from, to); err != nil {
		return Entry{}, pathError(err, "The source or target path does not exist.")
	}
	info, err := root.Lstat(to)
	if err != nil {
		return Entry{}, pathError(err, "The moved entry could not be read back.")
	}
	return entryFor(path.Base(to), info), nil
}

// Chmod applies a permission set given as three octal digits ("755"). The
// range excludes setuid/setgid/sticky bits by construction, and symlinks and
// special files are rejected so only regular files and directories change.
func (o *HostOperator) Chmod(_ context.Context, scope sitefs.Scope, rawPath, mode string) (Entry, error) {
	permissions, err := ParseMode(mode)
	if err != nil {
		return Entry{}, err
	}
	root, relative, err := o.resolveMutable(scope, rawPath)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil {
		return Entry{}, pathError(err, "The requested path does not exist.")
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return Entry{}, invalid("Only regular files and directories can have their permissions changed.")
	}
	if err := root.Chmod(relative, permissions); err != nil {
		return Entry{}, pathError(err, "The requested path does not exist.")
	}
	updated, err := root.Lstat(relative)
	if err != nil {
		return Entry{}, pathError(err, "The changed entry could not be read back.")
	}
	return entryFor(path.Base(relative), updated), nil
}

// ParseMode validates a three-octal-digit permission string such as "640".
func ParseMode(mode string) (fs.FileMode, error) {
	if len(mode) != 3 {
		return 0, invalid("The mode must be three octal digits, such as 755.")
	}
	value, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return 0, invalid("The mode must be three octal digits, such as 755.")
	}
	return fs.FileMode(value), nil
}

func (o *HostOperator) Copy(_ context.Context, scope sitefs.Scope, rawFrom, rawTo string) (CopyResult, error) {
	root, from, err := o.resolve(scope, rawFrom)
	if err != nil {
		return CopyResult{}, err
	}
	defer root.Close()
	to, err := relativeMutable(rawTo)
	if err != nil {
		return CopyResult{}, err
	}
	source, err := root.Lstat(from)
	if err != nil {
		return CopyResult{}, pathError(err, "The source path does not exist.")
	}
	if _, err := root.Lstat(to); err == nil {
		return CopyResult{}, existsConflict("An entry already exists at the target path.")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return CopyResult{}, pathError(err, "The target path is not reachable.")
	}
	budget := newBudget("copy", copyMaxEntries, copyMaxBytes)
	result := &CopyResult{}
	switch {
	case source.Mode().IsRegular():
		err = o.copyFile(scope, root, from, to, source, budget, result)
	case source.IsDir():
		if to == from || strings.HasPrefix(to, from+"/") {
			return CopyResult{}, invalid("A directory cannot be copied into itself.")
		}
		err = o.copyTree(scope, root, from, to, budget, result)
	default:
		return CopyResult{}, invalid("Only regular files and directories can be copied.")
	}
	if err != nil {
		return CopyResult{}, err
	}
	return *result, nil
}

func (o *HostOperator) copyFile(scope sitefs.Scope, root *os.Root, from, to string, info fs.FileInfo, budget *budget, result *CopyResult) error {
	if err := budget.spendEntry(); err != nil {
		return err
	}
	if err := budget.spendBytes(info.Size()); err != nil {
		return err
	}
	source, err := root.Open(from)
	if err != nil {
		return pathError(err, "The source path does not exist.")
	}
	defer source.Close()
	if err := o.stageFile(scope, root, to, io.LimitReader(source, info.Size()), info.Mode().Perm()); err != nil {
		return err
	}
	result.Copied++
	result.Bytes += info.Size()
	return nil
}

func (o *HostOperator) copyTree(scope sitefs.Scope, root *os.Root, from, to string, budget *budget, result *CopyResult) error {
	if err := budget.spendEntry(); err != nil {
		return err
	}
	if err := root.Mkdir(to, directoryMode); err != nil {
		return pathError(err, "The target directory does not exist.")
	}
	if err := o.chown(scope, to); err != nil {
		return err
	}
	result.Copied++
	directory, err := root.Open(from)
	if err != nil {
		return pathError(err, "The source path does not exist.")
	}
	children, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		info, err := child.Info()
		if err != nil {
			result.Skipped++
			continue
		}
		childFrom, childTo := path.Join(from, child.Name()), path.Join(to, child.Name())
		switch {
		case info.Mode().IsRegular():
			if err := o.copyFile(scope, root, childFrom, childTo, info, budget, result); err != nil {
				return err
			}
		case info.IsDir():
			if err := o.copyTree(scope, root, childFrom, childTo, budget, result); err != nil {
				return err
			}
		default:
			result.Skipped++
		}
	}
	return nil
}

func (o *HostOperator) Delete(_ context.Context, scope sitefs.Scope, rawPath string, recursive bool) error {
	root, relative, err := o.resolveMutable(scope, rawPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(relative); err != nil {
		return pathError(err, "The requested path does not exist.")
	}
	if recursive {
		if err := root.RemoveAll(relative); err != nil {
			return err
		}
		return nil
	}
	if err := root.Remove(relative); err != nil {
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return invalid("The directory is not empty; use a recursive delete.")
		}
		return err
	}
	return nil
}

func (o *HostOperator) DirectorySize(_ context.Context, scope sitefs.Scope, rawPath string) (SizeResult, error) {
	root, relative, err := o.resolve(scope, rawPath)
	if err != nil {
		return SizeResult{}, err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil {
		return SizeResult{}, pathError(err, "The requested path does not exist.")
	}
	if !info.IsDir() {
		return SizeResult{}, invalid("The requested path is not a directory.")
	}
	result := SizeResult{}
	remaining := sizeMaxEntries
	var walk func(dir string)
	walk = func(dir string) {
		directory, err := root.Open(dir)
		if err != nil {
			return
		}
		// The walk can never account for more entries than the budget allows, so
		// the budget is also the right bound on how much of one directory to read.
		children, unread, err := readDirLimited(directory, remaining)
		directory.Close()
		if err != nil {
			return
		}
		defer func() {
			if unread {
				result.Truncated = true
			}
		}()
		for _, child := range children {
			if result.Truncated {
				return
			}
			if remaining == 0 {
				result.Truncated = true
				return
			}
			remaining--
			info, err := child.Info()
			if err != nil {
				continue
			}
			switch {
			case info.IsDir():
				result.Dirs++
				walk(path.Join(dir, child.Name()))
			case info.Mode().IsRegular():
				result.Files++
				result.Bytes += info.Size()
			default:
				result.Files++
			}
		}
	}
	walk(relative)
	return result, nil
}

// resolve validates the scope, confines resolution beneath the site root,
// and normalizes the client path. Callers must Close the returned root.
func (o *HostOperator) resolve(scope sitefs.Scope, rawPath string) (*os.Root, string, error) {
	if err := sitefs.Validate(scope, o.SiteRoot); err != nil {
		return nil, "", invalid("The site scope is invalid: " + err.Error() + ".")
	}
	relative, err := sitefs.CleanRelative(rawPath)
	if err != nil {
		return nil, "", invalid("The path is not allowed: " + err.Error() + ".")
	}
	root, err := sitefs.OpenRoot(scope)
	if err != nil {
		return nil, "", notFound("The site directory is not present on this node.")
	}
	return root, relative, nil
}

func (o *HostOperator) resolveMutable(scope sitefs.Scope, rawPath string) (*os.Root, string, error) {
	root, relative, err := o.resolve(scope, rawPath)
	if err != nil {
		return nil, "", err
	}
	if err := mutable(relative); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, relative, nil
}

func relativeMutable(rawPath string) (string, error) {
	relative, err := sitefs.CleanRelative(rawPath)
	if err != nil {
		return "", invalid("The path is not allowed: " + err.Error() + ".")
	}
	if err := mutable(relative); err != nil {
		return "", err
	}
	return relative, nil
}

// mutable enforces the write zone: mutations must name something strictly
// inside public/, private/, tmp/, or backups/.
func mutable(relative string) error {
	first, rest, _ := strings.Cut(relative, "/")
	if _, ok := writeZones[first]; !ok || rest == "" {
		return invalid("Changes are only allowed inside the public, private, tmp, and backups directories.")
	}
	return nil
}

func (o *HostOperator) makeDirectories(scope sitefs.Scope, root *os.Root, relative string) error {
	current := ""
	for _, segment := range strings.Split(relative, "/") {
		current = path.Join(current, segment)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(current, directoryMode); err != nil {
				return pathError(err, "The requested path does not exist.")
			}
			if err := o.chown(scope, current); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return pathError(err, "The requested path does not exist.")
		}
		if !info.IsDir() {
			return invalid("An existing file blocks the requested directory path.")
		}
	}
	return nil
}

// stageFile writes content to a temporary file beside the target, fsyncs,
// renames into place, and hands ownership to the site account.
func (o *HostOperator) stageFile(scope sitefs.Scope, root *os.Root, relative string, content io.Reader, mode fs.FileMode) error {
	staging := path.Join(path.Dir(relative), ".nexa-stage-"+randomID())
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return pathError(err, "The target directory does not exist.")
	}
	discard := func(cause error) error {
		file.Close()
		_ = root.Remove(staging)
		return cause
	}
	if _, err := io.Copy(file, content); err != nil {
		return discard(err)
	}
	if err := file.Sync(); err != nil {
		return discard(err)
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(staging)
		return err
	}
	if err := root.Rename(staging, relative); err != nil {
		_ = root.Remove(staging)
		return pathError(err, "The target directory does not exist.")
	}
	return o.chown(scope, relative)
}

func (o *HostOperator) chown(scope sitefs.Scope, relative string) error {
	return o.Owner.Chown(filepath.Join(scope.RootPath, filepath.FromSlash(relative)), scope.UnixUser)
}

func openRegular(root *os.Root, relative string) (*os.File, fs.FileInfo, error) {
	info, err := root.Stat(relative)
	if err != nil {
		return nil, nil, pathError(err, "The requested file does not exist.")
	}
	if info.IsDir() {
		return nil, nil, invalid("The requested path is a directory.")
	}
	if !info.Mode().IsRegular() {
		return nil, nil, invalid("Only regular files can be read.")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, pathError(err, "The requested file does not exist.")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		file.Close()
		return nil, nil, invalid("Only regular files can be read.")
	}
	return file, opened, nil
}

func requireRegular(info fs.FileInfo, action string) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return invalid("Symbolic links cannot be " + action + ".")
	}
	if info.IsDir() {
		return invalid("A directory exists at the target path.")
	}
	if !info.Mode().IsRegular() {
		return invalid("Only regular files can be " + action + ".")
	}
	return nil
}

func fileETag(root *os.Root, relative string) (string, error) {
	file, err := root.Open(relative)
	if err != nil {
		return "", pathError(err, "The requested file does not exist.")
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func entryFor(name string, info fs.FileInfo) Entry {
	kind := "other"
	switch {
	case info.Mode().IsRegular():
		kind = "file"
	case info.IsDir():
		kind = "dir"
	case info.Mode()&fs.ModeSymlink != 0:
		kind = "symlink"
	}
	owner, group := ownerNames(info)
	return Entry{Name: name, Kind: kind, Size: info.Size(), Mode: info.Mode().Perm().String()[1:], Owner: owner, Group: group, ModifiedAt: info.ModTime().UTC()}
}

func ownerNames(info fs.FileInfo) (string, string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	owner := strconv.FormatUint(uint64(stat.Uid), 10)
	group := strconv.FormatUint(uint64(stat.Gid), 10)
	if account, err := user.LookupId(owner); err == nil {
		owner = account.Username
	}
	if resolved, err := user.LookupGroupId(group); err == nil {
		group = resolved.Name
	}
	return owner, group
}

func pathError(err error, missing string) error {
	var operationErr *OperationError
	if errors.As(err, &operationErr) {
		return operationErr
	}
	if errors.Is(err, fs.ErrNotExist) {
		return notFound(missing)
	}
	if errors.Is(err, fs.ErrExist) {
		return existsConflict("An entry already exists at the target path.")
	}
	if strings.Contains(err.Error(), "escapes from parent") {
		return invalid("The path escapes the site root.")
	}
	return err
}

type budget struct {
	operation string
	entries   int
	bytes     int64
}

func newBudget(operation string, entries int, bytes int64) *budget {
	return &budget{operation: operation, entries: entries, bytes: bytes}
}

func (b *budget) spendEntry() error {
	if b.entries == 0 {
		return invalid(fmt.Sprintf("The %s exceeds the entry limit.", b.operation))
	}
	b.entries--
	return nil
}

func (b *budget) spendBytes(size int64) error {
	if size > b.bytes {
		return invalid(fmt.Sprintf("The %s exceeds the size limit.", b.operation))
	}
	b.bytes -= size
	return nil
}

func randomID() string { return secureid.Hex(16) }
