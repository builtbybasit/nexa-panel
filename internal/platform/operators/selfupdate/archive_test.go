package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostileArchive builds a tarball from raw headers so a test can express a
// member no legitimate release job would ever produce.
func hostileArchive(t *testing.T, members []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	for index := range members {
		header := members[index]
		body := bodies[header.Name]
		if header.Typeflag == tar.TypeReg {
			header.Size = int64(len(body))
		}
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatalf("write header %q: %v", header.Name, err)
		}
		if body != "" {
			if _, err := writer.Write([]byte(body)); err != nil {
				t.Fatalf("write body %q: %v", header.Name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func TestExtractReleaseStripsTopLevelAndSetsModes(t *testing.T) {
	archive := releaseArchive(t, "0.4.0", []byte("nexa-binary"), nil)
	destination := filepath.Join(t.TempDir(), "staging")
	if err := extractRelease(archive, destination); err != nil {
		t.Fatalf("extract: %v", err)
	}
	// The versioned top-level directory is stripped, so the layout is stable
	// regardless of the version in the archive's own directory name.
	binary, err := os.ReadFile(filepath.Join(destination, "bin", "nexa"))
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(binary) != "nexa-binary" {
		t.Fatalf("extracted binary = %q", binary)
	}
	installer, err := os.Stat(filepath.Join(destination, "scripts", "install.sh"))
	if err != nil {
		t.Fatalf("stat installer: %v", err)
	}
	// The installer has to be runnable; the agent runs with UMask=0177, so the
	// mode is only right if the extractor set it explicitly.
	if installer.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installer mode = %v, want owner-executable", installer.Mode().Perm())
	}
	if installer.Mode().Perm()&0o077 != 0 {
		t.Fatalf("installer mode = %v, want root-only", installer.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(destination, "packaging", "systemd", "nexa-agent.service")); err != nil {
		t.Fatalf("packaging tree should be extracted: %v", err)
	}
}

// Every script the release bundle ships as executable has to survive extraction
// executable. install.sh refuses to apply packaging without `-x` on the staged
// uninstaller ("no executable uninstaller at ..."), and it resolves the release
// helper the same way, so a member left at 0600 here fails the update on the
// node rather than in any test. The list mirrors the executables the release
// workflow asserts on the built bundle.
func TestExtractReleaseMakesEveryBundledExecutableRunnable(t *testing.T) {
	archive := releaseArchive(t, "0.4.0", []byte("nexa-binary"), nil)
	destination := filepath.Join(t.TempDir(), "staging")
	if err := extractRelease(archive, destination); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, entry := range []string{
		"bin/nexa",
		"scripts/install.sh",
		"scripts/nexa-seed-admin.sh",
		"scripts/nexa-release-helper.py",
		"scripts/uninstall.sh",
	} {
		info, err := os.Stat(filepath.Join(destination, filepath.FromSlash(entry)))
		if err != nil {
			t.Fatalf("stat %s: %v", entry, err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("%s mode = %v, want owner-executable", entry, info.Mode().Perm())
		}
		// Executable must not mean reachable by anyone else: the staged tree is
		// root-only until the installer copies out of it.
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode = %v, want root-only", entry, info.Mode().Perm())
		}
	}
}

func TestExtractReleaseRejectsPathTraversal(t *testing.T) {
	archive := hostileArchive(t, []tar.Header{
		{Name: "nexa-panel-0.4.0-linux-amd64/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "nexa-panel-0.4.0-linux-amd64/../../etc/cron.d/pwn", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"nexa-panel-0.4.0-linux-amd64/../../etc/cron.d/pwn": "* * * * * root sh\n"})

	root := t.TempDir()
	destination := filepath.Join(root, "staging")
	err := extractRelease(archive, destination)
	if err == nil {
		t.Fatal("expected a traversing member to be rejected")
	}
	if !strings.Contains(err.Error(), "traversing") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "etc")); statErr == nil {
		t.Fatal("the traversing member escaped the staging directory")
	}
}

func TestExtractReleaseRejectsAbsolutePaths(t *testing.T) {
	archive := hostileArchive(t, []tar.Header{
		{Name: "/etc/shadow", Typeflag: tar.TypeReg, Mode: 0o600},
	}, map[string]string{"/etc/shadow": "root::0:0:::\n"})

	if err := extractRelease(archive, filepath.Join(t.TempDir(), "staging")); err == nil {
		t.Fatal("expected an absolute member to be rejected")
	}
}

func TestExtractReleaseRejectsSymlinks(t *testing.T) {
	// The classic escape: a symlink out of the tree, followed by a write
	// through it. Refusing the link type is what stops the second half.
	archive := hostileArchive(t, []tar.Header{
		{Name: "nexa-panel-0.4.0-linux-amd64/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "nexa-panel-0.4.0-linux-amd64/bin/nexa", Typeflag: tar.TypeSymlink, Linkname: "/usr/bin/env", Mode: 0o777},
	}, nil)

	destination := filepath.Join(t.TempDir(), "staging")
	err := extractRelease(archive, destination)
	if err == nil {
		t.Fatal("expected a symlink member to be rejected")
	}
	if !strings.Contains(err.Error(), "not a regular file or directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(destination, "bin", "nexa")); statErr == nil {
		t.Fatal("the symlink must not have been created")
	}
}

func TestExtractReleaseRejectsHardLinksAndDevices(t *testing.T) {
	for _, typeflag := range []byte{tar.TypeLink, tar.TypeChar, tar.TypeFifo} {
		archive := hostileArchive(t, []tar.Header{
			{Name: "nexa-panel-0.4.0-linux-amd64/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "nexa-panel-0.4.0-linux-amd64/bin/nexa", Typeflag: typeflag, Linkname: "/usr/bin/nexa", Mode: 0o755},
		}, nil)
		if err := extractRelease(archive, filepath.Join(t.TempDir(), "staging")); err == nil {
			t.Fatalf("expected tar type %q to be rejected", typeflag)
		}
	}
}

func TestExtractReleaseRejectsMembersOutsideTheLayout(t *testing.T) {
	archive := releaseArchive(t, "0.4.0", []byte("nexa-binary"), map[string][]byte{
		"etc/sudoers.d/nexa": []byte("nexa ALL=(ALL) NOPASSWD: ALL\n"),
	})
	err := extractRelease(archive, filepath.Join(t.TempDir(), "staging"))
	if err == nil {
		t.Fatal("expected a member outside the release layout to be rejected")
	}
	if !strings.Contains(err.Error(), "outside the expected release layout") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractReleaseRejectsSecondTopLevelDirectory(t *testing.T) {
	archive := hostileArchive(t, []tar.Header{
		{Name: "nexa-panel-0.4.0-linux-amd64/bin/nexa", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "elsewhere/bin/nexa", Typeflag: tar.TypeReg, Mode: 0o755},
	}, map[string]string{
		"nexa-panel-0.4.0-linux-amd64/bin/nexa": "real",
		"elsewhere/bin/nexa":                    "impostor",
	})
	if err := extractRelease(archive, filepath.Join(t.TempDir(), "staging")); err == nil {
		t.Fatal("expected a second top-level directory to be rejected")
	}
}

func TestExtractReleaseRequiresBinaryAndInstaller(t *testing.T) {
	archive := hostileArchive(t, []tar.Header{
		{Name: "nexa-panel-0.4.0-linux-amd64/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "nexa-panel-0.4.0-linux-amd64/RELEASE", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"nexa-panel-0.4.0-linux-amd64/RELEASE": "version=0.4.0\n"})
	if err := extractRelease(archive, filepath.Join(t.TempDir(), "staging")); err == nil {
		t.Fatal("expected an archive without bin/nexa to be rejected")
	}
}

func TestExtractReleaseRejectsNonGzip(t *testing.T) {
	if err := extractRelease([]byte("this is not a tarball"), filepath.Join(t.TempDir(), "staging")); err == nil {
		t.Fatal("expected a non-gzip payload to be rejected")
	}
}
