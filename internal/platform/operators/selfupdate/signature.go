package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	maxSignatureBytes     = 64 * 1024
	maxAllowedSignerBytes = 64 * 1024
)

// verifyReleaseSignature proves that the exact archive bytes were signed by a
// key pinned on the node. The checksum still detects accidental corruption;
// this detached OpenSSH signature is the independent authenticity boundary.
func (o *HostOperator) verifyReleaseSignature(ctx context.Context, archive, signature []byte) error {
	if err := validateAllowedSigners(o.allowedSignersPath); err != nil {
		return err
	}
	if len(signature) == 0 || len(signature) > maxSignatureBytes {
		return errors.New("release signature is empty or exceeds the size limit")
	}
	if err := os.MkdirAll(o.workRoot, 0o700); err != nil {
		return fmt.Errorf("create the self-update work directory: %w", err)
	}
	if err := os.Chmod(o.workRoot, 0o700); err != nil {
		return fmt.Errorf("secure the self-update work directory: %w", err)
	}
	signatureFile, err := os.CreateTemp(o.workRoot, ".release-*.sig")
	if err != nil {
		return fmt.Errorf("stage the release signature: %w", err)
	}
	signaturePath := signatureFile.Name()
	defer os.Remove(signaturePath)
	if err := signatureFile.Chmod(0o600); err != nil {
		_ = signatureFile.Close()
		return fmt.Errorf("secure the release signature: %w", err)
	}
	if _, err := signatureFile.Write(signature); err != nil {
		_ = signatureFile.Close()
		return fmt.Errorf("write the release signature: %w", err)
	}
	if err := signatureFile.Sync(); err != nil {
		_ = signatureFile.Close()
		return fmt.Errorf("flush the release signature: %w", err)
	}
	if err := signatureFile.Close(); err != nil {
		return fmt.Errorf("close the release signature: %w", err)
	}
	output, err := o.runner.Run(ctx, Command{
		Name: "ssh-keygen",
		Args: []string{
			"-Y", "verify",
			"-f", o.allowedSignersPath,
			"-I", releaseSignerIdentity,
			"-n", releaseSignatureNamespace,
			"-s", signaturePath,
		},
		Stdin: archive,
	})
	if err != nil {
		return fmt.Errorf("release signature verification failed: %s: %w", lastLine(string(output)), err)
	}
	return nil
}

// validateAllowedSigners refuses replaceable or writable trust roots. A public
// key is not secret, but accepting a file another account can rewrite would
// reduce signature verification to a checksum fetched from the same attacker.
func validateAllowedSigners(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("release allowed-signers path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("read release allowed-signers file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("release allowed-signers path must be a regular file, not a symlink")
	}
	if info.Size() <= 0 || info.Size() > maxAllowedSignerBytes {
		return errors.New("release allowed-signers file is empty or exceeds the size limit")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("release allowed-signers file must not be group- or world-writable")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 && os.Geteuid() == 0 {
		return errors.New("release allowed-signers file must be owned by root")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read release allowed-signers file: %w", err)
	}
	if !strings.Contains(string(contents), releaseSignerIdentity) {
		return fmt.Errorf("release allowed-signers file has no %s identity", releaseSignerIdentity)
	}
	return nil
}
