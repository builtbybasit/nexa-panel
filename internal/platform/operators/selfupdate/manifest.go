package selfupdate

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// releaseManifestEntry is the bundle's own statement of what it is, written by
// scripts/build-linux-release.sh and asserted by the release workflow. It is a
// small key=value file:
//
//	version=v0.5.0
//	commit=1a2b3c4d5e6f
//	arch=amd64
//	built_at=2026-07-22T09:08:07Z
const releaseManifestEntry = "RELEASE"

// maxManifestBytes caps the manifest. It is four short lines; anything larger is
// not the file the build wrote.
const maxManifestBytes = 8 * 1024

// releaseManifest is the parsed RELEASE stamp.
type releaseManifest struct {
	Version string
	Commit  string
	Arch    string
	BuiltAt string
}

// readReleaseManifest loads the RELEASE stamp out of an extracted release tree.
func readReleaseManifest(path string) (releaseManifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("the release archive's %s could not be read: %w", releaseManifestEntry, err)
	}
	if !info.Mode().IsRegular() {
		return releaseManifest{}, fmt.Errorf("the release archive's %s is not a regular file", releaseManifestEntry)
	}
	if info.Size() > maxManifestBytes {
		return releaseManifest{}, fmt.Errorf("the release archive's %s is too large to be a release stamp", releaseManifestEntry)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("the release archive's %s could not be read: %w", releaseManifestEntry, err)
	}
	return parseReleaseManifest(string(raw))
}

// parseReleaseManifest reads the key=value stamp. Unknown keys are ignored so a
// later build can add a field without stranding older nodes, but a repeated key
// is refused: two disagreeing values for the same fact is exactly the ambiguity
// the agreement check exists to remove.
func parseReleaseManifest(contents string) (releaseManifest, error) {
	var manifest releaseManifest
	seen := map[string]bool{}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return releaseManifest{}, fmt.Errorf("the release archive's %s has a line that is not key=value", releaseManifestEntry)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			return releaseManifest{}, fmt.Errorf("the release archive's %s declares %s twice", releaseManifestEntry, key)
		}
		seen[key] = true
		switch key {
		case "version":
			manifest.Version = value
		case "commit":
			manifest.Commit = value
		case "arch":
			manifest.Arch = value
		case "built_at":
			manifest.BuiltAt = value
		}
	}
	for name, value := range map[string]string{
		"version":  manifest.Version,
		"commit":   manifest.Commit,
		"arch":     manifest.Arch,
		"built_at": manifest.BuiltAt,
	} {
		if value == "" {
			return releaseManifest{}, fmt.Errorf("the release archive's %s does not record %s", releaseManifestEntry, name)
		}
	}
	return manifest, nil
}

// verifyReleaseAgreement refuses a bundle unless every independent statement of
// what it is says the same thing.
//
// Each of these is written by a different step, and a mismatch between any two
// means the pipeline mixed artifacts — a tag re-pointed at another build, an
// arm64 tarball published under the amd64 asset name, a signature valid over
// bytes other than the ones being extracted. The signature proves the bundle was
// produced by the release key; it does not prove it is the bundle this node
// asked for, and only these comparisons do:
//
//   - the release tag,
//   - the version the release metadata resolved from that tag,
//   - the version in the bundle's own RELEASE stamp,
//   - the version the extracted binary reports when executed,
//   - the architecture in the RELEASE stamp,
//   - this host's architecture,
//   - the asset name the metadata resolved for that architecture,
//   - the SHA-256 that was checksum-verified and signature-verified.
func verifyReleaseAgreement(release Release, manifest releaseManifest, arch, binaryVersion, digest string, archive []byte) error {
	suffix, ok := assetArch(arch)
	if !ok {
		return fmt.Errorf("self-update is unsupported on this architecture")
	}
	if strings.TrimSpace(release.Tag) != "v"+release.Version {
		return fmt.Errorf("release tag %q does not name version %s", release.Tag, release.Version)
	}
	expectedAsset := fmt.Sprintf("nexa-panel-linux-%s.tar.gz", suffix)
	if release.AssetName != expectedAsset {
		return fmt.Errorf("release %s published the downloaded bundle as %q, not %s", release.Tag, release.AssetName, expectedAsset)
	}
	if manifest.Arch != suffix {
		return fmt.Errorf("the downloaded bundle is built for %s, not this node's %s", manifest.Arch, suffix)
	}
	manifestVersion := normalizeVersion(manifest.Version)
	if manifestVersion == "" {
		return fmt.Errorf("the downloaded bundle records the unrecognizable version %q", manifest.Version)
	}
	if manifestVersion != release.Version {
		return fmt.Errorf("the downloaded bundle is version %s, but release %s was requested", manifestVersion, release.Tag)
	}
	reported := normalizeVersion(binaryVersion)
	if reported == "" {
		return fmt.Errorf("the bundled binary reports the unrecognizable version %q", binaryVersion)
	}
	if reported != release.Version {
		return fmt.Errorf("the bundled binary reports version %s, not the released %s", reported, release.Version)
	}
	sum := sha256.Sum256(archive)
	actual := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(digest))) != 1 {
		return fmt.Errorf("the extracted bundle is not the bytes whose digest was verified")
	}
	return nil
}
