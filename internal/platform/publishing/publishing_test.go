package publishing

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadRoundTripEveryPublicationMode(t *testing.T) {
	cases := []struct {
		name      string
		state     State
		mode      string
		listen    string
		encrypted bool
	}{
		{
			name:      "panel-terminated TLS",
			state:     State{Hostname: "panel.example.com", Port: 443, TLS: true},
			mode:      "tls",
			listen:    "443 ssl",
			encrypted: true,
		},
		{
			name:      "TLS terminated in front of the node",
			state:     State{Hostname: "panel.example.com", Port: 80, ExternalTLS: true},
			mode:      "external-tls",
			listen:    "80",
			encrypted: true,
		},
		{
			name:      "loopback quick start",
			state:     State{Hostname: "localhost", ListenAddress: "127.0.0.1", Port: 8888},
			mode:      "loopback",
			listen:    "127.0.0.1:8888",
			encrypted: false,
		},
		{
			name:      "explicit plaintext test node",
			state:     State{Hostname: "_", Port: 8888},
			mode:      "plaintext",
			listen:    "8888",
			encrypted: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "publishing.json")
			if err := Save(path, testCase.state); err != nil {
				t.Fatalf("save: %v", err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if loaded.Hostname != testCase.state.Hostname || loaded.Port != testCase.state.Port ||
				loaded.TLS != testCase.state.TLS || loaded.ExternalTLS != testCase.state.ExternalTLS ||
				loaded.ListenAddress != testCase.state.ListenAddress {
				t.Fatalf("round trip changed the record: %+v", loaded)
			}
			if loaded.Mode() != testCase.mode {
				t.Fatalf("mode = %q, want %q", loaded.Mode(), testCase.mode)
			}
			if loaded.ListenDirective() != testCase.listen {
				t.Fatalf("listen directive = %q, want %q", loaded.ListenDirective(), testCase.listen)
			}
			if loaded.Encrypted() != testCase.encrypted {
				t.Fatalf("encrypted = %t, want %t", loaded.Encrypted(), testCase.encrypted)
			}
			// Readable by an operator, writable only by root.
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("record mode = %v, want 0644", info.Mode().Perm())
			}
		})
	}
}

func TestLoadReportsAMissingRecordDistinctlyFromAFailure(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("missing record = %v, want ErrNoRecord", err)
	}
	// A truncated or hand-mangled record is a failure, never a fresh node: a
	// caller that treated it as "never published" would republish the panel from
	// defaults and could take a TLS node back to plaintext.
	path := filepath.Join(t.TempDir(), "publishing.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"hostname":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || errors.Is(err, ErrNoRecord) {
		t.Fatalf("truncated record = %v, want a decode failure", err)
	}
}

func TestValidateRefusesRecordsThatCannotDescribeAPublication(t *testing.T) {
	cases := []struct {
		name  string
		state State
	}{
		{"no hostname", State{Version: 1, Port: 443, TLS: true}},
		{"port out of range", State{Version: 1, Hostname: "panel.example.com", Port: 0}},
		{"both TLS terminations", State{Version: 1, Hostname: "panel.example.com", Port: 443, TLS: true, ExternalTLS: true}},
		{"hostname nginx would reject", State{Version: 1, Hostname: "panel.example.com; root /etc", Port: 443, TLS: true}},
		{"future record version", State{Version: currentVersion + 1, Hostname: "panel.example.com", Port: 443, TLS: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.state.Validate(); err == nil {
				t.Fatal("an unusable record validated")
			}
			// Save must refuse it too, so a bad record never reaches disk where the
			// next read would have to reject it instead.
			if err := Save(filepath.Join(t.TempDir(), "publishing.json"), testCase.state); err == nil {
				t.Fatal("an unusable record was written")
			}
		})
	}
}

// The Certbot-mutated vhost is the exact file the old inference had to read.
// The migration still reads it, but only once.
func TestInferFromVhostRecoversACertbotTLSPublication(t *testing.T) {
	vhost := `limit_req_zone $binary_remote_addr zone=nexa_auth:10m rate=10r/m;
server {
    server_name panel.example.com;
    include /etc/nginx/snippets/nexa-panel-proxy.conf;
    listen [::]:443 ssl ipv6only=on; # managed by Certbot
    listen 443 ssl; # managed by Certbot
    ssl_certificate /etc/letsencrypt/live/panel.example.com/fullchain.pem;
}
server {
    listen 80;
    server_name panel.example.com;
    return 301 https://$host$request_uri;
}
`
	state, err := InferFromVhost(strings.NewReader(vhost))
	if err != nil {
		t.Fatalf("infer: %v", err)
	}
	if !state.TLS || state.Port != 443 || state.Hostname != "panel.example.com" {
		t.Fatalf("inferred %+v", state)
	}
	// Certbot printed the IPv6 companion listener first. Recording "::" as the
	// bind address would republish the panel IPv6-only.
	if state.ListenAddress != "" || state.ListenDirective() != "443 ssl" {
		t.Fatalf("listener = %q (%q), want every interface on 443 ssl", state.ListenAddress, state.ListenDirective())
	}
	// The Certbot redirect block listens on 80; reading it instead of the panel's
	// own server block is exactly the misread that would republish a TLS node in
	// plaintext.
	if state.Mode() != "tls" {
		t.Fatalf("mode = %q, want tls", state.Mode())
	}
}

func TestInferFromVhostRecoversTheLoopbackQuickStart(t *testing.T) {
	vhost := `server {
    listen 127.0.0.1:8888;
    listen [::1]:8888;
    server_name localhost;
}
`
	state, err := InferFromVhost(strings.NewReader(vhost))
	if err != nil {
		t.Fatalf("infer: %v", err)
	}
	if state.ListenAddress != "127.0.0.1" || state.Port != 8888 || state.TLS || state.Mode() != "loopback" {
		t.Fatalf("inferred %+v", state)
	}
}

func TestInferFromVhostKeepsOnlyTheCanonicalServerName(t *testing.T) {
	state, err := InferFromVhost(strings.NewReader("server {\n listen 80;\n server_name panel.example.com www.panel.example.com;\n}\n"))
	if err != nil {
		t.Fatalf("infer: %v", err)
	}
	if state.Hostname != "panel.example.com" {
		t.Fatalf("hostname = %q", state.Hostname)
	}
}

func TestInferFromVhostRefusesAFileItCannotRead(t *testing.T) {
	if _, err := InferFromVhost(strings.NewReader("# the panel was never published here\n")); err == nil {
		t.Fatal("a vhost with no listener produced a publishing state")
	}
	if _, err := InferFromVhost(strings.NewReader("server {\n listen unix:/run/x.sock;\n server_name panel;\n}\n")); err == nil {
		t.Fatal("a listener this record cannot describe was accepted")
	}
}

func TestMigrateRecoversOnceAndThenReadsTheRecord(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "publishing.json")
	vhostPath := filepath.Join(directory, "nexa-panel.conf")
	if err := os.WriteFile(vhostPath, []byte("server {\n listen 443 ssl; # managed by Certbot\n server_name panel.example.com;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	state, migrated, err := Migrate(statePath, vhostPath, now)
	if err != nil || !migrated {
		t.Fatalf("first migrate = %+v, migrated %t, err %v", state, migrated, err)
	}
	if state.Source != "vhost-migration" || !state.TLS {
		t.Fatalf("migrated state = %+v", state)
	}

	// The vhost now says something else. The record, not the file, is the answer:
	// this is the whole point of the change.
	if err := os.WriteFile(vhostPath, []byte("server {\n listen 8888;\n server_name _;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, migratedAgain, err := Migrate(statePath, vhostPath, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if migratedAgain {
		t.Fatal("migrate re-read the vhost on a node that already has a record")
	}
	if !second.TLS || second.Hostname != "panel.example.com" || second.Port != 443 {
		t.Fatalf("the record was overwritten from the vhost: %+v", second)
	}
}

func TestMigrateOnANodeThatWasNeverPublishedReportsNoRecord(t *testing.T) {
	directory := t.TempDir()
	_, _, err := Migrate(filepath.Join(directory, "publishing.json"), filepath.Join(directory, "absent.conf"), time.Now())
	if !errors.Is(err, ErrNoRecord) {
		t.Fatalf("migrate without a vhost = %v, want ErrNoRecord", err)
	}
}

// A failed write must not leave a half-record behind, and must not destroy the
// record already in place.
func TestSaveLeavesNoStagedFileBehind(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "publishing.json")
	if err := Save(path, State{Hostname: "panel.example.com", Port: 443, TLS: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := Save(path, State{Hostname: "panel.example.com", Port: 80, ExternalTLS: true}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "publishing.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory holds %v, want only the record", names)
	}
	loaded, err := Load(path)
	if err != nil || !loaded.ExternalTLS || loaded.Port != 80 {
		t.Fatalf("rewritten record = %+v, err %v", loaded, err)
	}
}
