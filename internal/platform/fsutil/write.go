// Package fsutil holds small filesystem helpers shared across operators.
package fsutil

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// writeConfig holds the resolved options for Write.
type writeConfig struct {
	mkdirPerm os.FileMode // when nonzero, MkdirAll the parent with this perm
	hasMkdir  bool
	uid, gid  int // both >= 0 to chown the temp file before publishing
	hasOwner  bool
}

// Option customises Write.
type Option func(*writeConfig)

// WithMkdirAll creates the parent directory (and any missing ancestors) with the
// given permission before writing, mirroring os.MkdirAll semantics (a no-op when
// the directory already exists).
func WithMkdirAll(perm os.FileMode) Option {
	return func(c *writeConfig) {
		c.mkdirPerm = perm
		c.hasMkdir = true
	}
}

// WithOwner chowns the temporary file to uid/gid before it is renamed into
// place, so ownership is applied atomically with publication. This avoids a
// privileged path-based chown after rename in directories writable by
// unprivileged accounts. Both values must be >= 0 to take effect.
func WithOwner(uid, gid int) Option {
	return func(c *writeConfig) {
		c.uid = uid
		c.gid = gid
		c.hasOwner = true
	}
}

// Write atomically publishes content to path. It writes a sibling temporary
// file, fsyncs it, renames it into place, then fsyncs the parent directory so
// the rename itself survives a crash — a reader never observes a half-written
// or missing file, even across power loss.
//
// The temporary file uses a hidden ("." prefixed) name so watchers that ignore
// dotfiles (sshd drop-ins, nginx sites-enabled) never pick it up mid-write.
func Write(path string, content []byte, mode os.FileMode, opts ...Option) error {
	var cfg writeConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	directory := filepath.Dir(path)
	if cfg.hasMkdir {
		if err := os.MkdirAll(directory, cfg.mkdirPerm); err != nil {
			return err
		}
	}

	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".nexa-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(name)
		}
	}()

	if _, err := io.Copy(temporary, bytes.NewReader(content)); err != nil {
		return err
	}
	// Chown before chmod: chown can clear setuid/setgid bits, so applying the
	// mode afterwards keeps the caller's intended permissions.
	if cfg.hasOwner && cfg.uid >= 0 && cfg.gid >= 0 {
		if err := temporary.Chown(cfg.uid, cfg.gid); err != nil {
			return err
		}
	}
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	keep = true

	// Fsync the parent directory so the rename (the entry that now points at the
	// new inode) is durable, not just the file contents.
	if handle, openErr := os.Open(directory); openErr == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
