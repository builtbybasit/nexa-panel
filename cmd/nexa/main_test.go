package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestConfigureLoggingReadsFlagsBeforeTheCommand(t *testing.T) {
	output := &bytes.Buffer{}
	args, logger, err := configureLogging([]string{"--log-level", "debug", "--log-format=text", "api", "--address", "127.0.0.1:9000"}, output)
	if err != nil {
		t.Fatalf("configureLogging returned an error: %v", err)
	}
	if strings.Join(args, " ") != "api --address 127.0.0.1:9000" {
		t.Fatalf("remaining args = %v, want the subcommand and its own flags", args)
	}
	logger.Debug("visible")
	if !strings.Contains(output.String(), "level=DEBUG msg=visible") {
		t.Fatalf("log output = %q, want a debug line in text format", output.String())
	}
}

func TestConfigureLoggingDefaultsToInfoJSON(t *testing.T) {
	output := &bytes.Buffer{}
	args, logger, err := configureLogging([]string{"doctor"}, output)
	if err != nil {
		t.Fatalf("configureLogging returned an error: %v", err)
	}
	if len(args) != 1 || args[0] != "doctor" {
		t.Fatalf("remaining args = %v, want [doctor]", args)
	}
	logger.Debug("hidden")
	logger.Info("shown")
	if strings.Contains(output.String(), "hidden") {
		t.Fatalf("log output = %q, want debug suppressed at the default level", output.String())
	}
	if !strings.Contains(output.String(), `"level":"INFO"`) {
		t.Fatalf("log output = %q, want JSON", output.String())
	}
}

func TestConfigureLoggingPrefersFlagsOverTheEnvironment(t *testing.T) {
	t.Setenv("NEXA_LOG_LEVEL", "error")
	t.Setenv("NEXA_LOG_FORMAT", "text")
	output := &bytes.Buffer{}
	if _, logger, err := configureLogging([]string{"--log-level", "warn", "agent"}, output); err != nil {
		t.Fatalf("configureLogging returned an error: %v", err)
	} else {
		logger.Warn("audible")
	}
	if !strings.Contains(output.String(), "level=WARN msg=audible") {
		t.Fatalf("log output = %q, want the flag level with the environment format", output.String())
	}
}

func TestConfigureLoggingAppliesTheEnvironmentWithoutFlags(t *testing.T) {
	t.Setenv("NEXA_LOG_LEVEL", "debug")
	output := &bytes.Buffer{}
	_, logger, err := configureLogging([]string{"api"}, output)
	if err != nil {
		t.Fatalf("configureLogging returned an error: %v", err)
	}
	logger.Debug("verbose")
	if !strings.Contains(output.String(), "verbose") {
		t.Fatalf("log output = %q, want NEXA_LOG_LEVEL honoured", output.String())
	}
}

func TestConfigureLoggingRejectsUnknownValues(t *testing.T) {
	if _, _, err := configureLogging([]string{"--log-level", "trace", "api"}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unknown log level must be a startup error, not a silent fallback")
	}
	if _, _, err := configureLogging([]string{"--log-format", "logfmt", "api"}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unknown log format must be a startup error")
	}
	if _, _, err := configureLogging([]string{"--log-level"}, &bytes.Buffer{}); err == nil {
		t.Fatal("a value-less --log-level must be an error")
	}
	t.Setenv("NEXA_LOG_LEVEL", "chatty")
	if _, _, err := configureLogging([]string{"api"}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unknown NEXA_LOG_LEVEL must be a startup error")
	}
}

func TestConfigureLoggingLeavesOtherLeadingFlagsAlone(t *testing.T) {
	args, _, err := configureLogging([]string{"--help"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("configureLogging returned an error: %v", err)
	}
	if len(args) != 1 || args[0] != "--help" {
		t.Fatalf("remaining args = %v, want --help left for the dispatcher", args)
	}
}

func TestRunRequiresACommand(t *testing.T) {
	if err := run(nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err == nil {
		t.Fatal("run without a command should fail")
	}
}

// TestRunMainLeavesStdoutToTheCommand pins the half of the `nexa doctor --json`
// contract that lives outside doctor: the failing exit path logs after the
// report has been written, so scripts/install.sh only parses valid JSON if that
// log line — and every other one — stays off stdout.
func TestRunMainLeavesStdoutToTheCommand(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })

	logs := &bytes.Buffer{}
	code := runMain([]string{"no-such-command"}, logs)

	writer.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(written) != 0 {
		t.Fatalf("stdout = %q, want nothing but the command's own output", written)
	}
	if !strings.Contains(logs.String(), "nexa stopped") {
		t.Fatalf("log output = %q, want the failure reported on the log stream", logs.String())
	}
}
