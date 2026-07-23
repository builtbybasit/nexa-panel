package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/publishing"
)

func writePublishingRecord(t *testing.T, directory string, state publishing.State) string {
	t.Helper()
	path := filepath.Join(directory, "publishing.json")
	if err := publishing.Save(path, state); err != nil {
		t.Fatalf("save record: %v", err)
	}
	return path
}

func TestPublishingRequiresASubcommand(t *testing.T) {
	if err := runPublishing(nil); err == nil {
		t.Fatal("a bare `nexa publishing` was accepted")
	}
	if err := runPublishing([]string{"republish"}); err == nil {
		t.Fatal("an unknown publishing subcommand was accepted")
	}
}

func TestPublishingShowRendersTheRecordForTheInstaller(t *testing.T) {
	directory := t.TempDir()
	statePath := writePublishingRecord(t, directory, publishing.State{
		Hostname: "panel.example.com", Port: 443, TLS: true, Source: "install",
	})

	output := &bytes.Buffer{}
	if err := runPublishingShow([]string{"--state-file", statePath, "--shell"}, output); err != nil {
		t.Fatalf("show --shell: %v", err)
	}
	// These are the assignments the installer evals in place of sed-ing the vhost.
	for _, want := range []string{
		"NEXA_PUBLISH_HOSTNAME='panel.example.com'",
		"NEXA_PUBLISH_LISTEN='443 ssl'",
		"NEXA_PUBLISH_PORT=443",
		"NEXA_PUBLISH_TLS=1",
		"NEXA_PUBLISH_EXTERNAL_TLS=0",
		"NEXA_PUBLISH_MODE='tls'",
		"NEXA_PUBLISH_RECORDED=1",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("show --shell output missing %q:\n%s", want, output.String())
		}
	}

	output.Reset()
	if err := runPublishingShow([]string{"--state-file", statePath, "--json"}, output); err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var decoded publishing.State
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode show --json: %v (%s)", err, output.String())
	}
	if decoded.Hostname != "panel.example.com" || !decoded.TLS {
		t.Fatalf("decoded record = %+v", decoded)
	}
}

// A node that predates the record must still answer, and must say plainly that
// the answer is a guess rather than the record.
func TestPublishingShowFallsBackToTheVhostAndSaysSo(t *testing.T) {
	directory := t.TempDir()
	vhostPath := filepath.Join(directory, "nexa-panel.conf")
	if err := os.WriteFile(vhostPath, []byte("server {\n listen 8888;\n server_name _;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "publishing.json")

	output := &bytes.Buffer{}
	if err := runPublishingShow([]string{"--state-file", statePath, "--vhost", vhostPath}, output); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(output.String(), "inferred from") || !strings.Contains(output.String(), "plaintext") {
		t.Fatalf("show output = %q", output.String())
	}
	// show never writes: recovering the record is `migrate`'s explicit job.
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("show wrote a record: %v", err)
	}

	output.Reset()
	if err := runPublishingShow([]string{"--state-file", statePath, "--vhost", vhostPath, "--shell"}, output); err != nil {
		t.Fatalf("show --shell: %v", err)
	}
	if !strings.Contains(output.String(), "NEXA_PUBLISH_RECORDED=0") {
		t.Fatalf("an inferred state was reported as recorded:\n%s", output.String())
	}
}

func TestPublishingShowFailsWhenThereIsNothingToRead(t *testing.T) {
	directory := t.TempDir()
	err := runPublishingShow([]string{
		"--state-file", filepath.Join(directory, "publishing.json"),
		"--vhost", filepath.Join(directory, "absent.conf"),
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("show invented a publishing state out of nothing")
	}
}

func TestPublishingShowRejectsBothRenderings(t *testing.T) {
	if err := runPublishingShow([]string{"--json", "--shell"}, &bytes.Buffer{}); err == nil {
		t.Fatal("--json and --shell were accepted together")
	}
}

func TestPublishingWritesAreRootOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this test asserts the refusal shown to a non-root operator")
	}
	directory := t.TempDir()
	statePath := filepath.Join(directory, "publishing.json")
	setErr := runPublishingSet([]string{"--state-file", statePath, "--hostname", "panel.example.com", "--tls"}, &bytes.Buffer{})
	if setErr == nil || !strings.Contains(setErr.Error(), "must be run as root") {
		t.Fatalf("set as a non-root user = %v", setErr)
	}
	migrateErr := runPublishingMigrate([]string{"--state-file", statePath}, &bytes.Buffer{})
	if migrateErr == nil || !strings.Contains(migrateErr.Error(), "must be run as root") {
		t.Fatalf("migrate as a non-root user = %v", migrateErr)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("a refused write still created the record")
	}
}

func TestPublishingSetDefaultsThePortToTheScheme(t *testing.T) {
	// `publishing set --tls` without a port must not publish TLS on 80.
	if publishing.DefaultPort(true) != 443 || publishing.DefaultPort(false) != 80 {
		t.Fatalf("default ports = %d/%d, want 443/80", publishing.DefaultPort(true), publishing.DefaultPort(false))
	}
}
