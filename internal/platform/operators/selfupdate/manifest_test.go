package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestParseReleaseManifestReadsTheStamp(t *testing.T) {
	manifest, err := parseReleaseManifest("# built by scripts/build-linux-release.sh\nversion=v0.5.0\ncommit=1a2b3c4d5e6f\narch=amd64\nbuilt_at=2026-07-22T09:08:07Z\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if manifest.Version != "v0.5.0" || manifest.Commit != "1a2b3c4d5e6f" || manifest.Arch != "amd64" || manifest.BuiltAt != "2026-07-22T09:08:07Z" {
		t.Fatalf("parsed %+v", manifest)
	}
}

func TestParseReleaseManifestRejectsMalformedStamps(t *testing.T) {
	for name, contents := range map[string]string{
		"no version":     "commit=abc\narch=amd64\nbuilt_at=now\n",
		"no commit":      "version=v0.5.0\narch=amd64\nbuilt_at=now\n",
		"no arch":        "version=v0.5.0\ncommit=abc\nbuilt_at=now\n",
		"no built_at":    "version=v0.5.0\ncommit=abc\narch=amd64\n",
		"empty value":    "version=\ncommit=abc\narch=amd64\nbuilt_at=now\n",
		"not key=value":  "version=v0.5.0\ncommit abc\narch=amd64\nbuilt_at=now\n",
		"repeated key":   "version=v0.5.0\nversion=v9.9.9\ncommit=abc\narch=amd64\nbuilt_at=now\n",
		"repeated arch":  "version=v0.5.0\ncommit=abc\narch=amd64\narch=arm64\nbuilt_at=now\n",
		"nothing at all": "",
	} {
		if _, err := parseReleaseManifest(contents); err == nil {
			t.Fatalf("%s: expected the stamp to be refused", name)
		}
	}
}

func TestParseReleaseManifestIgnoresUnknownKeys(t *testing.T) {
	// A later build adding a field must not strand a node that predates it.
	if _, err := parseReleaseManifest("version=v0.5.0\ncommit=abc\narch=amd64\nbuilt_at=now\nsbom=spdx\n"); err != nil {
		t.Fatalf("an unknown key should be ignored, got %v", err)
	}
}

// agreeingRelease is the fully consistent set the agreement check must accept,
// so each mismatch test can change exactly one fact.
func agreeingRelease() (Release, releaseManifest, []byte, string) {
	archive := []byte("release-bundle-bytes")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	release := Release{Version: "0.5.0", Tag: "v0.5.0", AssetName: amd64AssetName}
	manifest := releaseManifest{Version: "v0.5.0", Commit: "abc123", Arch: "amd64", BuiltAt: "2026-07-22T09:08:07Z"}
	return release, manifest, archive, digest
}

func TestVerifyReleaseAgreementAcceptsAConsistentBundle(t *testing.T) {
	release, manifest, archive, digest := agreeingRelease()
	if err := verifyReleaseAgreement(release, manifest, "amd64", "0.5.0", digest, archive); err != nil {
		t.Fatalf("a consistent bundle was refused: %v", err)
	}
}

func TestVerifyReleaseAgreementRejectsEveryMismatch(t *testing.T) {
	// Each case breaks exactly one of the independent statements of what the
	// bundle is. A signature proves the bytes are a genuine release; only these
	// comparisons prove they are the release this node asked for.
	cases := map[string]struct {
		mutate func(*Release, *releaseManifest, *string, *string, *[]byte)
		want   string
	}{
		"tag does not name the resolved version": {
			mutate: func(r *Release, _ *releaseManifest, _, _ *string, _ *[]byte) { r.Tag = "v0.6.0" },
			want:   "does not name version",
		},
		"tag is not in the release form": {
			mutate: func(r *Release, _ *releaseManifest, _, _ *string, _ *[]byte) { r.Tag = "0.5.0" },
			want:   "does not name version",
		},
		"asset name is for another architecture": {
			mutate: func(r *Release, _ *releaseManifest, _, _ *string, _ *[]byte) {
				r.AssetName = "nexa-panel-linux-arm64.tar.gz"
			},
			want: "published the downloaded bundle as",
		},
		"asset name is not a bundle at all": {
			mutate: func(r *Release, _ *releaseManifest, _, _ *string, _ *[]byte) { r.AssetName = "nexa-linux-amd64" },
			want:   "published the downloaded bundle as",
		},
		"RELEASE arch is not this host's": {
			mutate: func(_ *Release, m *releaseManifest, _, _ *string, _ *[]byte) { m.Arch = "arm64" },
			want:   "built for arm64",
		},
		"RELEASE version disagrees with the release": {
			mutate: func(_ *Release, m *releaseManifest, _, _ *string, _ *[]byte) { m.Version = "v0.4.0" },
			want:   "is version 0.4.0",
		},
		"RELEASE version is not a version": {
			mutate: func(_ *Release, m *releaseManifest, _, _ *string, _ *[]byte) { m.Version = "nightly" },
			want:   "unrecognizable version",
		},
		"binary reports another version": {
			mutate: func(_ *Release, _ *releaseManifest, _, binary *string, _ *[]byte) {
				*binary = "0.4.0"
			},
			want: "reports version 0.4.0",
		},
		"binary reports no version": {
			mutate: func(_ *Release, _ *releaseManifest, _, binary *string, _ *[]byte) { *binary = "dev" },
			want:   "unrecognizable version",
		},
		"host architecture is unsupported": {
			mutate: func(_ *Release, _ *releaseManifest, arch, _ *string, _ *[]byte) { *arch = "riscv64" },
			want:   "unsupported on this architecture",
		},
		"extracted bytes are not the verified ones": {
			mutate: func(_ *Release, _ *releaseManifest, _, _ *string, archive *[]byte) {
				*archive = append(*archive, '!')
			},
			want: "not the bytes whose digest was verified",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			release, manifest, archive, digest := agreeingRelease()
			arch := "amd64"
			binaryVersion := "0.5.0"
			testCase.mutate(&release, &manifest, &arch, &binaryVersion, &archive)
			err := verifyReleaseAgreement(release, manifest, arch, binaryVersion, digest, archive)
			if err == nil {
				t.Fatal("the mismatch was accepted")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not explain the mismatch (want %q)", err, testCase.want)
			}
		})
	}
}

func TestApplyRefusesABundleWhoseStampDisagreesBeforeRunningItsInstaller(t *testing.T) {
	// The end-to-end guarantee: a bundle that passes checksum and signature but
	// claims another architecture never reaches scripts/install.sh, which the
	// operator runs as root.
	binary := []byte("new-nexa-binary-bytes")
	archive := releaseArchive(t, "0.2.0", binary, map[string][]byte{
		releaseManifestEntry: []byte("version=0.2.0\ncommit=abc123def456\narch=arm64\nbuilt_at=2026-07-22T09:08:07Z\n"),
	})
	source := fakeSource{release: testRelease()}
	downloader := fakeDownloader{assets: releaseAssets(archive)}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc, built now)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	_, err := operator.Apply(context.Background(), Change{})
	if err == nil || !strings.Contains(err.Error(), "built for arm64") {
		t.Fatalf("apply: %v, want the architecture mismatch to be refused", err)
	}
	if packagingSync(runner) != nil {
		t.Fatal("the release installer must not run for a bundle that failed the agreement check")
	}
	live, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read live binary: %v", err)
	}
	if string(live) != "old-binary" {
		t.Fatalf("the live binary was replaced with %q", live)
	}
}

func TestApplyRefusesABundleWithNoStamp(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	archive := releaseArchive(t, "0.2.0", binary, map[string][]byte{
		releaseManifestEntry: []byte("version=0.2.0\n"),
	})
	operator, _ := newTestOperator(t, "0.1.0", fakeSource{release: testRelease()}, fakeDownloader{assets: releaseAssets(archive)}, &fakeRunner{versionOutput: "0.2.0 (commit abc)"})

	if _, err := operator.Apply(context.Background(), Change{}); err == nil || !strings.Contains(err.Error(), "does not record") {
		t.Fatalf("apply: %v, want an incomplete RELEASE stamp to be refused", err)
	}
}
