package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The release tarball is extracted as root, from bytes that came off the
// network. Even though the checksum ties those bytes to the release metadata,
// the extractor treats every member as hostile: a tar is a format that can name
// "../../etc/shadow", a symlink to /usr/bin, a 40 GB sparse file, or a device
// node, and the only defence that holds is refusing anything outside the layout
// a release is known to have.
const (
	// maxExtractedBytes caps the total uncompressed size, defusing a gzip bomb
	// that is small enough to pass maxArchiveBytes.
	maxExtractedBytes = 512 * 1024 * 1024
	// maxArchiveEntries caps the member count; a real release tree is a few
	// dozen files.
	maxArchiveEntries = 4096
	// maxEntryPathLength bounds a single member's path.
	maxEntryPathLength = 512
)

// releaseTreePrefixes are the directories a release tarball may populate, and
// releaseTreeFiles the top-level files it may carry. Anything else is refused
// rather than written: the operator only ever consumes bin/nexa, the packaging
// tree, and the scripts, so a member outside them is either a mistake in the
// release job or an attempt to drop a file somewhere useful.
var (
	releaseTreePrefixes = []string{"bin/", "packaging/", "scripts/"}
	releaseTreeFiles    = []string{"RELEASE"}
)

// executableEntries are the extracted members that must be runnable. Everything
// else lands 0600 and every directory 0700 — the staged tree is root-only, and
// the agent's UMask=0177 means modes have to be set explicitly rather than
// inferred from the mkdir/create arguments.
var executableEntries = map[string]bool{
	releaseBinaryEntry:           true,
	releaseInstallerEntry:        true,
	"scripts/nexa-seed-admin.sh": true,
}

// extractRelease unpacks a release tarball into destination, stripping the
// single versioned top-level directory the archive is wrapped in so the result
// is always destination/bin/nexa, destination/packaging/..., regardless of the
// version in the archive's own directory name.
//
// It refuses: absolute or traversing paths, anything that is not a regular file
// or directory (symlinks, hardlinks, devices, FIFOs), members outside the known
// release layout, more than maxArchiveEntries members, and more than
// maxExtractedBytes of content.
func extractRelease(archive []byte, destination string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("the release archive is not a gzip stream: %w", err)
	}
	defer gzipReader.Close()

	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create the release staging directory: %w", err)
	}
	// MkdirAll respects the process umask, and the agent runs with UMask=0177,
	// so the mode is set explicitly or the tree comes out unreadable.
	if err := os.Chmod(destination, 0o700); err != nil {
		return fmt.Errorf("secure the release staging directory: %w", err)
	}

	reader := tar.NewReader(gzipReader)
	var (
		entries   int
		extracted int64
		root      string
		sawBinary bool
	)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read the release archive: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("the release archive holds more than %d entries", maxArchiveEntries)
		}
		relative, err := releaseEntryPath(header.Name, &root)
		if err != nil {
			return err
		}
		if relative == "" {
			// The archive's own top-level directory; the destination stands in
			// for it.
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(relative)), 0o700); err != nil {
				return fmt.Errorf("create %s from the release archive: %w", relative, err)
			}
		case tar.TypeReg:
			if header.Size < 0 || extracted+header.Size > maxExtractedBytes {
				return fmt.Errorf("the release archive expands beyond the %d byte limit", int64(maxExtractedBytes))
			}
			written, err := writeArchiveFile(reader, destination, relative, header.Size)
			if err != nil {
				return err
			}
			extracted += written
			if relative == releaseBinaryEntry {
				sawBinary = true
			}
		default:
			// Symlinks, hardlinks, devices and FIFOs have no place in a release
			// tree and are the classic way out of an extraction directory.
			return fmt.Errorf("the release archive contains %s, which is not a regular file or directory", relative)
		}
	}
	if !sawBinary {
		return fmt.Errorf("the release archive has no %s", releaseBinaryEntry)
	}
	if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(releaseInstallerEntry))); err != nil {
		return fmt.Errorf("the release archive has no %s", releaseInstallerEntry)
	}
	return nil
}

// releaseEntryPath validates one member name and returns its path relative to
// the archive's single top-level directory, which it also pins on the first
// member seen. It returns "" for the top-level directory itself.
func releaseEntryPath(name string, root *string) (string, error) {
	if len(name) > maxEntryPathLength {
		return "", errors.New("the release archive contains an implausibly long path")
	}
	// tar paths are always slash-separated; a backslash is not a separator here
	// but is a legal filename character, so it is refused rather than reasoned
	// about.
	if strings.ContainsAny(name, "\\\x00") {
		return "", errors.New("the release archive contains an unusable path")
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("the release archive contains the absolute path %s", name)
	}
	cleaned := path.Clean(strings.TrimSuffix(name, "/"))
	if cleaned == "." || cleaned == "" {
		return "", nil
	}
	// path.Clean leaves a leading "..", which is the traversal to refuse.
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("the release archive contains the traversing path %s", name)
	}
	parts := strings.SplitN(cleaned, "/", 2)
	if *root == "" {
		*root = parts[0]
	}
	if parts[0] != *root {
		return "", fmt.Errorf("the release archive has more than one top-level directory (%s and %s)", *root, parts[0])
	}
	if len(parts) == 1 {
		return "", nil
	}
	relative := parts[1]
	if !isReleaseTreeEntry(relative) {
		return "", fmt.Errorf("the release archive contains %s, which is outside the expected release layout", relative)
	}
	return relative, nil
}

// isReleaseTreeEntry reports whether a member path belongs to the layout a
// release is defined to have.
func isReleaseTreeEntry(relative string) bool {
	for _, allowed := range releaseTreeFiles {
		if relative == allowed {
			return true
		}
	}
	for _, prefix := range releaseTreePrefixes {
		if strings.HasPrefix(relative, prefix) && len(relative) > len(prefix) {
			return true
		}
		// The prefix directory itself arrives as its own member.
		if relative == strings.TrimSuffix(prefix, "/") {
			return true
		}
	}
	return false
}

// writeArchiveFile writes one member, creating its parent directories and
// setting the mode explicitly. The copy is bounded by the header size + 1 so a
// member whose body outruns its declared size is refused rather than trusted.
func writeArchiveFile(reader io.Reader, destination, relative string, size int64) (int64, error) {
	target := filepath.Join(destination, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return 0, fmt.Errorf("create the parent of %s: %w", relative, err)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("write %s from the release archive: %w", relative, err)
	}
	written, err := io.Copy(file, io.LimitReader(reader, size+1))
	if err != nil {
		_ = file.Close()
		return 0, fmt.Errorf("write %s from the release archive: %w", relative, err)
	}
	if err := file.Close(); err != nil {
		return 0, fmt.Errorf("flush %s from the release archive: %w", relative, err)
	}
	if written > size {
		return 0, fmt.Errorf("the release archive entry %s is larger than it declares", relative)
	}
	mode := os.FileMode(0o600)
	if executableEntries[relative] {
		mode = 0o700
	}
	if err := os.Chmod(target, mode); err != nil {
		return 0, fmt.Errorf("set the mode of %s: %w", relative, err)
	}
	return written, nil
}
