// Package backups is the node-agent operator for the backups module. It owns
// the single rclone-based transport that every backup account speaks through:
// a backup account is just an rclone remote definition (type + params), so
// Local, S3, SFTP and Google Drive are all reached with one code path instead
// of four bespoke clients.
package backups

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// AccountType enumerates the rclone backends a backup account can use. The
// value is passed verbatim as the remote's `type`, so it must be a real rclone
// backend name.
const (
	TypeLocal = "local"
	TypeS3    = "s3"
	TypeSFTP  = "sftp"
	TypeDrive = "drive" // Google Drive
)

// Account is the resolved view of a backup account handed to the agent. The
// control plane decrypts the account's secrets and merges them into Params
// before calling, so the operator never touches ciphertext.
type Account struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params"`
}

// TestResult reports whether an account's storage is reachable and writable.
// A failed connection is a normal, expected outcome (bad credentials, wrong
// endpoint): it is returned as OK=false with a message, NOT as an error. An
// error is reserved for the operator itself malfunctioning.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Operator is the agent-side contract the control plane calls over the Unix
// socket. It grows one method per phase (TestAccount now; Run/Restore/List/
// Delete/InstallSchedule later).
type Operator interface {
	TestAccount(ctx context.Context, account Account) (TestResult, error)
	Run(ctx context.Context, request RunRequest) (Manifest, error)
	Restore(ctx context.Context, request RestoreRequest) error
	DeleteCopy(ctx context.Context, request DeleteRequest) error
	InstallSchedule(ctx context.Context, spec ScheduleSpec) error
	RemoveSchedule(ctx context.Context, planID string) error
}

// Runner executes a command with an augmented environment and returns its
// stdout, or an error carrying the trimmed stderr. rclone reads its remote
// configuration from RCLONE_CONFIG_* environment variables, which keeps
// credentials off both the argv (visible in ps) and the disk.
type Runner interface {
	Run(ctx context.Context, name string, args, env []string) (string, error)
	// RunToFile streams a command's stdout to outPath (for dump tools that write
	// to stdout, where buffering a whole database in memory would be reckless).
	RunToFile(ctx context.Context, name string, args, env []string, outPath string) error
	// RunFromFile feeds inPath to a command's stdin (for clients like `mysql`
	// that restore by reading a SQL script from stdin).
	RunFromFile(ctx context.Context, name string, args, env []string, inPath string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), errors.New(message)
	}
	return stdout.String(), nil
}

func (execRunner) RunToFile(ctx context.Context, name string, args, env []string, outPath string) error {
	file, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stdout = file
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

func (execRunner) RunFromFile(ctx context.Context, name string, args, env []string, inPath string) error {
	file, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer file.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stdin = file
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

// HostOperator is the real, privileged implementation. It shells out to rclone
// (backups) and systemctl (schedules).
type HostOperator struct {
	runner      Runner
	binary      string
	nexaBinary  string
	systemdRoot string
}

// NewHostOperator builds the operator. A nil runner uses the real exec runner;
// tests inject a fake. binary defaults to "rclone" on PATH; nexaBinary is the
// path this process was launched as (so generated timer services re-invoke the
// same binary), falling back to /usr/bin/nexa.
func NewHostOperator(runner Runner) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	nexaBinary := "/usr/bin/nexa"
	if executable, err := os.Executable(); err == nil && executable != "" {
		nexaBinary = executable
	}
	return &HostOperator{runner: runner, binary: "rclone", nexaBinary: nexaBinary, systemdRoot: systemdRoot}, nil
}

// TestAccount verifies the account is reachable AND writable by creating the
// backup directory (idempotent) then listing it. mkdir is a stronger probe
// than a bare list because a read-only or misconfigured remote fails it, and
// backups always need write access.
func (h *HostOperator) TestAccount(ctx context.Context, account Account) (TestResult, error) {
	remote, env, err := rcloneRemote(account)
	if err != nil {
		return TestResult{}, err
	}
	if _, err := h.runner.Run(ctx, h.binary, []string{"mkdir", remote}, env); err != nil {
		return TestResult{OK: false, Message: sanitize(err.Error())}, nil
	}
	if _, err := h.runner.Run(ctx, h.binary, []string{"lsd", remote}, env); err != nil {
		return TestResult{OK: false, Message: sanitize(err.Error())}, nil
	}
	return TestResult{OK: true, Message: "Connection succeeded."}, nil
}
