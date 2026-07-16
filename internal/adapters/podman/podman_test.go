package podman

import (
	"context"
	"errors"
	"testing"
)

func TestInspectorAvailable(t *testing.T) {
	inspector := Inspector{
		lookupPath: func(string) (string, error) { return "/usr/bin/podman", nil },
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("podman version 6.0.1\n"), nil
		},
	}

	status, err := inspector.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect returned an error: %v", err)
	}
	if !status.Available || status.Version != "6.0.1" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestInspectorUnavailable(t *testing.T) {
	inspector := Inspector{
		lookupPath: func(string) (string, error) { return "", errors.New("missing") },
	}

	status, err := inspector.Inspect(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if status.Available {
		t.Fatal("unavailable Podman reported as available")
	}
}
