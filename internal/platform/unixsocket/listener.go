// Package unixsocket owns safe lifecycle handling for local service sockets.
// It refuses to overwrite non-sockets and only removes the exact socket inode
// that it created, preventing stale-path cleanup from deleting replacement data.
package unixsocket

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
)

func Listen(path string, directoryMode, socketMode fs.FileMode) (net.Listener, func(), error) {
	if !filepath.IsAbs(path) {
		return nil, nil, errors.New("Unix socket path must be absolute")
	}
	if directoryMode.Perm() == 0 || socketMode.Perm() == 0 {
		return nil, nil, errors.New("Unix socket directory and socket modes are required")
	}
	if err := os.MkdirAll(filepath.Dir(path), directoryMode.Perm()); err != nil {
		return nil, nil, fmt.Errorf("create Unix socket directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("Unix socket path exists and is not a socket: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, nil, fmt.Errorf("remove stale Unix socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("inspect Unix socket: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on Unix socket: %w", err)
	}
	if err := os.Chmod(path, socketMode.Perm()); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, nil, fmt.Errorf("set Unix socket permissions: %w", err)
	}
	created, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, nil, fmt.Errorf("inspect created Unix socket: %w", err)
	}

	cleanup := func() {
		_ = listener.Close()
		current, statErr := os.Lstat(path)
		// SameFile compares device and inode, and an inode is reused as soon as
		// it is freed: remove this socket, write an ordinary file in the same
		// directory, and the replacement can land on the very inode the socket
		// had. It then satisfies SameFile and gets deleted — exactly the data
		// loss this cleanup exists to prevent. Requiring it to still be a socket
		// closes that, and mirrors the check Listen already makes before it
		// overwrites anything.
		if statErr == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(created, current) {
			_ = os.Remove(path)
		}
	}
	return listener, cleanup, nil
}
