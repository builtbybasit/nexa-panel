package backups

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxSiteArchiveEntries = 1_000_000
	maxSiteArchiveBytes   = int64(1 << 40) // 1 TiB expanded safety ceiling.
)

type archivedDirectory struct {
	name    string
	mode    fs.FileMode
	modTime time.Time
}

type archivedHardLink struct {
	name   string
	target string
}

// extractSiteArchive implements the privileged extraction boundary in Go so
// archive paths and links are validated before any entry is created. All file
// operations are confined by os.Root even if an archive changes a path into a
// symlink between entries.
func extractSiteArchive(archivePath, destination string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open site archive: %w", err)
	}
	defer archive.Close()

	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open compressed site archive: %w", err)
	}
	defer compressed.Close()

	root, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("confine site archive extraction: %w", err)
	}
	defer root.Close()

	reader := tar.NewReader(compressed)
	seen := make(map[string]struct{})
	directories := make([]archivedDirectory, 0)
	hardLinks := make([]archivedHardLink, 0)
	var entries int
	var expanded int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read site archive: %w", err)
		}
		entries++
		if entries > maxSiteArchiveEntries {
			return fmt.Errorf("site archive contains more than %d entries", maxSiteArchiveEntries)
		}
		name, rootEntry, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if rootEntry {
			if header.Typeflag != tar.TypeDir {
				return errors.New("site archive root entry is not a directory")
			}
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("site archive contains duplicate entry %q", header.Name)
		}
		seen[name] = struct{}{}

		if header.Size < 0 || header.Size > maxSiteArchiveBytes-expanded {
			return fmt.Errorf("site archive expands beyond %d bytes", maxSiteArchiveBytes)
		}
		expanded += header.Size
		mode := fs.FileMode(header.Mode) & 0o777
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureArchiveDirectory(root, name); err != nil {
				return fmt.Errorf("create archive directory %q: %w", name, err)
			}
			directories = append(directories, archivedDirectory{name: name, mode: mode, modTime: header.ModTime})
		case tar.TypeReg, 0: // POSIX regular file and legacy NUL regular-file flag.
			if err := ensureArchiveParent(root, name); err != nil {
				return fmt.Errorf("prepare archive file %q: %w", name, err)
			}
			if err := root.RemoveAll(name); err != nil {
				return fmt.Errorf("replace archive file %q: %w", name, err)
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("create archive file %q: %w", name, err)
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			if copyErr == nil {
				copyErr = file.Chmod(mode)
			}
			if copyErr == nil {
				copyErr = file.Sync()
			}
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return fmt.Errorf("write archive file %q: %w", name, errors.Join(copyErr, closeErr))
			}
			if !header.ModTime.IsZero() {
				if err := root.Chtimes(name, header.ModTime, header.ModTime); err != nil {
					return fmt.Errorf("set archive file time %q: %w", name, err)
				}
			}
		case tar.TypeSymlink:
			if err := validateArchiveLink(name, header.Linkname); err != nil {
				return err
			}
			if err := ensureArchiveParent(root, name); err != nil {
				return err
			}
			if err := root.RemoveAll(name); err != nil {
				return err
			}
			if err := root.Symlink(header.Linkname, name); err != nil {
				return fmt.Errorf("create archive symlink %q: %w", name, err)
			}
		case tar.TypeLink:
			target, rootTarget, err := safeArchivePath(header.Linkname)
			if err != nil || rootTarget {
				return fmt.Errorf("archive hard link %q has an invalid target", name)
			}
			hardLinks = append(hardLinks, archivedHardLink{name: name, target: target})
		default:
			return fmt.Errorf("site archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}

	for _, link := range hardLinks {
		if err := ensureArchiveParent(root, link.name); err != nil {
			return err
		}
		info, err := root.Lstat(link.target)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("archive hard link %q does not reference a regular extracted file", link.name)
		}
		if err := root.RemoveAll(link.name); err != nil {
			return err
		}
		if err := root.Link(link.target, link.name); err != nil {
			return fmt.Errorf("create archive hard link %q: %w", link.name, err)
		}
	}

	// Apply directory modes after all children are written so an archived 0500
	// directory cannot prevent extraction of its own contents.
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i].name, "/") > strings.Count(directories[j].name, "/")
	})
	for _, directory := range directories {
		if err := root.Chmod(directory.name, directory.mode); err != nil {
			return fmt.Errorf("set archive directory mode %q: %w", directory.name, err)
		}
		if !directory.modTime.IsZero() {
			if err := root.Chtimes(directory.name, directory.modTime, directory.modTime); err != nil {
				return fmt.Errorf("set archive directory time %q: %w", directory.name, err)
			}
		}
	}
	return nil
}

func safeArchivePath(value string) (name string, root bool, err error) {
	if value == "" || strings.ContainsAny(value, "\\\x00") || path.IsAbs(value) {
		return "", false, fmt.Errorf("site archive contains unsafe path %q", value)
	}
	name = path.Clean(value)
	if name == "." {
		return ".", true, nil
	}
	if name == ".." || strings.HasPrefix(name, "../") {
		return "", false, fmt.Errorf("site archive path %q escapes the document root", value)
	}
	return name, false, nil
}

func validateArchiveLink(name, target string) error {
	if target == "" || strings.ContainsAny(target, "\\\x00") || path.IsAbs(target) {
		return fmt.Errorf("archive symlink %q has unsafe target %q", name, target)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("archive symlink %q escapes the document root", name)
	}
	return nil
}

func ensureArchiveParent(root *os.Root, name string) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	parts := strings.Split(parent, "/")
	current := ""
	for _, part := range parts {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive parent %q is a symbolic link", current)
		}
		if !info.IsDir() {
			if err := root.RemoveAll(current); err != nil {
				return err
			}
			if err := root.Mkdir(current, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureArchiveDirectory(root *os.Root, name string) error {
	if err := ensureArchiveParent(root, name); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return root.Mkdir(name, 0o700)
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	if err := root.RemoveAll(name); err != nil {
		return err
	}
	return root.Mkdir(name, 0o700)
}

func validateStagedSiteTree(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == root || entry.IsDir() || entry.Type().IsRegular() {
			return nil
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return fmt.Errorf("restored site contains unsupported special file %s", name)
		}
		target, err := os.Readlink(name)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("restored site symlink %s has an absolute target", name)
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(name), target))
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("restored site symlink %s escapes the document root", name)
		}
		return nil
	})
}

func restoreRandomToken() (string, error) {
	value := make([]byte, 8)
	rand.Read(value)
	return hex.EncodeToString(value), nil
}

func syncParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
