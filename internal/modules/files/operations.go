package files

import (
	"context"

	"encoding/json"
	"errors"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// Job payloads embed the Scope that the HTTP handler already validated: job
// execution has no requesting user, so all authorization happens up front.
type archivePayload struct {
	Scope  sitefs.Scope `json:"scope"`
	Paths  []string     `json:"paths"`
	Target string       `json:"target"`
}

type extractPayload struct {
	Scope     sitefs.Scope `json:"scope"`
	Path      string       `json:"path"`
	TargetDir string       `json:"targetDir"`
}

type sizePayload struct {
	Scope sitefs.Scope `json:"scope"`
	Path  string       `json:"path"`
}

func (m *Module) archiveJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload archivePayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.Scope.SiteID == "" {
		return nil, errors.New("invalid persisted archive request")
	}
	if err := report(25, "Building the site archive."); err != nil {
		return nil, err
	}
	result, err := m.operator.Archive(ctx, payload.Scope, payload.Paths, payload.Target)
	if err != nil {
		return nil, err
	}
	if err := report(90, "The archive is written and owned by the site account."); err != nil {
		return nil, err
	}
	return result, nil
}

func (m *Module) extractJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload extractPayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.Scope.SiteID == "" {
		return nil, errors.New("invalid persisted extraction request")
	}
	if err := report(25, "Extracting the archive inside the site root."); err != nil {
		return nil, err
	}
	result, err := m.operator.Extract(ctx, payload.Scope, payload.Path, payload.TargetDir)
	if err != nil {
		return nil, err
	}
	if err := report(90, "Extracted entries are owned by the site account."); err != nil {
		return nil, err
	}
	return result, nil
}

func (m *Module) sizeJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload sizePayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.Scope.SiteID == "" {
		return nil, errors.New("invalid persisted directory size request")
	}
	if err := report(30, "Measuring the directory size."); err != nil {
		return nil, err
	}
	result, err := m.operator.DirectorySize(ctx, payload.Scope, payload.Path)
	if err != nil {
		return nil, err
	}
	return result, nil
}
