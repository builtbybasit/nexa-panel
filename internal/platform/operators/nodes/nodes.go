package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const ProbeKind = "nexa.probe.v1"

type Change struct {
	Present bool   `json:"present"`
	Content string `json:"content,omitempty"`
}

type Snapshot struct {
	Exists  bool   `json:"exists"`
	Digest  string `json:"digest,omitempty"`
	Content string `json:"content,omitempty"`
}

type Plan struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	Action    string    `json:"action"`
	Changed   bool      `json:"changed"`
	Before    Snapshot  `json:"before"`
	Desired   Snapshot  `json:"desired"`
	PlannedAt time.Time `json:"plannedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Signature string    `json:"signature,omitempty"`
}

type Operator interface {
	Plan(ctx context.Context, change Change) (Plan, error)
	Apply(ctx context.Context, plan Plan) (Snapshot, error)
	Observe(ctx context.Context) (Snapshot, error)
	Rollback(ctx context.Context, plan Plan) (Snapshot, error)
}

type FileOperator struct {
	target string
	now    func() time.Time
}

func NewFileOperator(target string) (*FileOperator, error) {
	if !filepath.IsAbs(target) {
		return nil, errors.New("probe target must be an absolute path")
	}
	return &FileOperator{target: filepath.Clean(target), now: time.Now}, nil
}

func (o *FileOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	if err := validateChange(change); err != nil {
		return Plan{}, err
	}
	before, err := o.Observe(ctx)
	if err != nil {
		return Plan{}, err
	}
	desired := Snapshot{Exists: change.Present}
	if change.Present {
		desired.Content = change.Content
		desired.Digest = digest([]byte(change.Content))
	}
	action := "none"
	switch {
	case !before.Exists && desired.Exists:
		action = "create"
	case before.Exists && !desired.Exists:
		action = "remove"
	case before.Exists && desired.Exists && before.Digest != desired.Digest:
		action = "update"
	}
	now := o.now().UTC()
	return Plan{
		ID: randomPlanID(), Kind: ProbeKind, Target: o.target, Action: action, Changed: action != "none",
		Before: before, Desired: desired, PlannedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}, nil
}

func (o *FileOperator) Apply(ctx context.Context, plan Plan) (Snapshot, error) {
	if err := o.validatePlan(plan); err != nil {
		return Snapshot{}, err
	}
	if o.now().UTC().After(plan.ExpiresAt) {
		return Snapshot{}, errors.New("operation plan has expired")
	}
	current, err := o.Observe(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !sameSnapshot(current, plan.Before) {
		return Snapshot{}, errors.New("managed state changed after planning; create a new plan")
	}
	if !plan.Changed {
		return current, nil
	}
	if err := o.writeSnapshot(plan.Desired); err != nil {
		return Snapshot{}, err
	}
	return o.Observe(ctx)
}

func (o *FileOperator) Observe(_ context.Context) (Snapshot, error) {
	info, err := os.Lstat(o.target)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect managed probe: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, errors.New("managed probe target is not a regular file")
	}
	if info.Size() > 4096 {
		return Snapshot{}, errors.New("managed probe exceeds the maximum size")
	}
	content, err := os.ReadFile(o.target)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read managed probe: %w", err)
	}
	return Snapshot{Exists: true, Digest: digest(content), Content: string(content)}, nil
}

func (o *FileOperator) Rollback(ctx context.Context, plan Plan) (Snapshot, error) {
	if err := o.validatePlan(plan); err != nil {
		return Snapshot{}, err
	}
	current, err := o.Observe(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !sameSnapshot(current, plan.Desired) {
		return Snapshot{}, errors.New("managed state changed after apply; automatic rollback is unsafe")
	}
	if err := o.writeSnapshot(plan.Before); err != nil {
		return Snapshot{}, err
	}
	return o.Observe(ctx)
}

func (o *FileOperator) validatePlan(plan Plan) error {
	if plan.ID == "" || plan.Kind != ProbeKind || plan.Target != o.target {
		return errors.New("operation plan is invalid for this agent")
	}
	if plan.Action != "none" && plan.Action != "create" && plan.Action != "update" && plan.Action != "remove" {
		return errors.New("operation plan action is invalid")
	}
	if plan.Desired.Exists {
		if err := validateChange(Change{Present: true, Content: plan.Desired.Content}); err != nil {
			return err
		}
		if plan.Desired.Digest != digest([]byte(plan.Desired.Content)) {
			return errors.New("operation plan desired digest is invalid")
		}
	}
	return nil
}

func (o *FileOperator) writeSnapshot(snapshot Snapshot) error {
	if !snapshot.Exists {
		if err := os.Remove(o.target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove managed probe: %w", err)
		}
		return nil
	}
	directory := filepath.Dir(o.target)
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed probe directory is unsafe")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create managed probe directory: %w", err)
		}
	} else {
		return fmt.Errorf("inspect managed probe directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".nexa-probe-*")
	if err != nil {
		return fmt.Errorf("create staged managed probe: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set managed probe permissions: %w", err)
	}
	if _, err := temporary.WriteString(snapshot.Content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write staged managed probe: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync staged managed probe: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged managed probe: %w", err)
	}
	if err := os.Rename(temporaryPath, o.target); err != nil {
		return fmt.Errorf("activate managed probe: %w", err)
	}
	return nil
}

func validateChange(change Change) error {
	if !change.Present {
		if change.Content != "" {
			return errors.New("absent probe state cannot include content")
		}
		return nil
	}
	if change.Content == "" || len(change.Content) > 256 || !utf8.ValidString(change.Content) || strings.ContainsRune(change.Content, '\x00') {
		return errors.New("probe content must be valid UTF-8 between 1 and 256 bytes")
	}
	return nil
}

func sameSnapshot(left, right Snapshot) bool {
	return left.Exists == right.Exists && left.Digest == right.Digest
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func randomPlanID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(value)
}
