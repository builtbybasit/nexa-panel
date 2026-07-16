package podman

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var ErrUnavailable = errors.New("podman is not installed or not discoverable")

type Status struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
}

type Inspector struct {
	lookupPath func(string) (string, error)
	run        func(context.Context, string, ...string) ([]byte, error)
}

func NewInspector() Inspector {
	return Inspector{
		lookupPath: exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
}

func (i Inspector) Inspect(ctx context.Context) (Status, error) {
	path, err := i.lookupPath("podman")
	if err != nil {
		return Status{Available: false}, ErrUnavailable
	}

	output, err := i.run(ctx, path, "--version")
	if err != nil {
		return Status{Available: false, Path: path}, fmt.Errorf("inspect podman: %w", err)
	}

	version := strings.TrimSpace(string(output))
	version = strings.TrimPrefix(version, "podman version ")
	if version == "" {
		return Status{Available: false, Path: path}, errors.New("podman returned an empty version")
	}

	return Status{Available: true, Version: version, Path: path}, nil
}
