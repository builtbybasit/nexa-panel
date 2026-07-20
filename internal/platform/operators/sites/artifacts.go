package sites

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

func (o *HostOperator) validatePlan(plan Plan) error {
	if plan.ID == "" || plan.Kind != PlanKind || len(plan.Artifacts) != len(plan.Before) {
		return errors.New("site activation plan is invalid")
	}
	expected, err := o.renderer.Render(plan.Site)
	if err != nil {
		return err
	}
	byKind := make(map[string]Artifact, len(expected.Artifacts))
	for _, artifact := range expected.Artifacts {
		byKind[artifact.Kind] = artifact
	}
	for index, artifact := range plan.Artifacts {
		exact, ok := byKind[artifact.Kind]
		if !ok || exact.Path != artifact.Path || exact.Mode != artifact.Mode || exact.Content != artifact.Content || plan.Before[index].Path != artifact.Path {
			return errors.New("site activation plan contains an unmanaged artifact")
		}
	}
	return nil
}

func (o *HostOperator) checkBefore(plan Plan) error {
	for _, expected := range plan.Before {
		current, err := readSnapshot(expected.Path)
		if err != nil {
			return err
		}
		if current.Exists != expected.Exists || current.Digest != expected.Digest {
			return errors.New("site state changed after planning; create a new plan")
		}
	}
	enabled, err := o.enabled(plan.Site.Slug)
	if err != nil {
		return err
	}
	if enabled != plan.EnabledBefore {
		return errors.New("Nginx site activation changed after planning; create a new plan")
	}
	return nil
}

func (o *HostOperator) writeArtifacts(artifacts []Artifact) error {
	for _, artifact := range artifacts {
		if err := atomicWrite(artifact.Path, []byte(artifact.Content), os.FileMode(artifact.Mode)); err != nil {
			return err
		}
	}
	return nil
}

func (o *HostOperator) restore(plan Plan) error {
	var failures []string
	for index := len(plan.Before) - 1; index >= 0; index-- {
		if err := writeSnapshot(plan.Before[index]); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if err := o.setEnabled(plan.Site.Slug, plan.EnabledBefore); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (o *HostOperator) observe(plan Plan) (Observation, error) {
	items := make([]Snapshot, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		snapshot, err := readSnapshot(artifact.Path)
		if err != nil {
			return Observation{}, err
		}
		items = append(items, snapshot)
	}
	enabled, err := o.enabled(plan.Site.Slug)
	if err != nil {
		return Observation{}, err
	}
	return Observation{SiteID: plan.Site.ID, Active: enabled, Artifacts: items, VerifiedAt: o.now().UTC()}, nil
}

func (o *HostOperator) enabled(slug string) (bool, error) {
	path := filepath.Join(o.enabledRoot, "nexa-"+slug+".conf")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, errors.New("Nginx enabled site path is not a symlink")
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(o.enabledRoot, target)
	}
	availableRoot := o.renderer.NginxAvailableRoot
	if availableRoot == "" {
		availableRoot = "/etc/nginx/sites-available"
	}
	expected := filepath.Join(availableRoot, "nexa-"+slug+".conf")
	if filepath.Clean(target) != filepath.Clean(expected) {
		return false, errors.New("Nginx enabled site symlink points outside its managed definition")
	}
	return true, nil
}

func (o *HostOperator) setEnabled(slug string, enabled bool) error {
	path := filepath.Join(o.enabledRoot, "nexa-"+slug+".conf")
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(o.enabledRoot, 0o755); err != nil {
		return err
	}
	if exists, err := o.enabled(slug); err != nil {
		return err
	} else if exists {
		return nil
	}
	availableRoot := o.renderer.NginxAvailableRoot
	if availableRoot == "" {
		availableRoot = "/etc/nginx/sites-available"
	}
	target := filepath.Join(availableRoot, "nexa-"+slug+".conf")
	return os.Symlink(target, path)
}

func readSnapshot(path string) (Snapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Path: path}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 512*1024 {
		return Snapshot{}, errors.New("managed site artifact is unsafe or too large")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Path: path, Exists: true, Mode: uint32(info.Mode().Perm()), Digest: digestBytes(content), Content: string(content)}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		snapshot.UID = int(stat.Uid)
		snapshot.GID = int(stat.Gid)
	}
	return snapshot, nil
}

func writeSnapshot(snapshot Snapshot) error {
	if !snapshot.Exists {
		if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return atomicWriteOwned(snapshot.Path, []byte(snapshot.Content), os.FileMode(snapshot.Mode), snapshot.UID, snapshot.GID)
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	return atomicWriteOwned(path, content, mode, -1, -1)
}

// atomicWriteOwned applies ownership to the temporary file descriptor before
// publishing it. This avoids a privileged path-based chown after rename in
// directories writable by managed site accounts.
func atomicWriteOwned(path string, content []byte, mode os.FileMode, uid, gid int) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".nexa-stage-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if uid >= 0 && gid >= 0 {
		if err := temporary.Chown(uid, gid); err != nil {
			_ = temporary.Close()
			return err
		}
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func digestBytes(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func digestString(content string) string { return digestBytes([]byte(content)) }

func randomID() string { return secureid.Hex(16) }
