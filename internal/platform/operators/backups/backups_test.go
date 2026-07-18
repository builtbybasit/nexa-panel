package backups

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records the commands rclone would have been invoked with.
type fakeRunner struct {
	calls  [][]string
	envs   [][]string
	output string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args, env []string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	return f.output, f.err
}

func (f *fakeRunner) RunToFile(_ context.Context, name string, args, env []string, outPath string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	return f.err
}

func (f *fakeRunner) RunFromFile(_ context.Context, name string, args, env []string, inPath string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	return f.err
}

func TestRcloneRemoteS3(t *testing.T) {
	remote, env, err := rcloneRemote(Account{
		Type: TypeS3, Path: "bucket/nightly",
		Params: map[string]string{"provider": "Minio", "secret_access_key": "s3cr3t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if remote != "nexa:bucket/nightly" {
		t.Fatalf("remote = %q", remote)
	}
	// Config must travel as env vars, never argv, so credentials stay off ps.
	assertEnv(t, env, "RCLONE_CONFIG_NEXA_TYPE=s3")
	assertEnv(t, env, "RCLONE_CONFIG_NEXA_PROVIDER=Minio")
	assertEnv(t, env, "RCLONE_CONFIG_NEXA_SECRET_ACCESS_KEY=s3cr3t")
}

func TestRcloneRemoteLocalRequiresAbsolute(t *testing.T) {
	if _, _, err := rcloneRemote(Account{Type: TypeLocal, Path: "relative"}); err == nil {
		t.Fatal("expected an error for a relative local path")
	}
	remote, _, err := rcloneRemote(Account{Type: TypeLocal, Path: "/srv/nexa/backups/"})
	if err != nil {
		t.Fatal(err)
	}
	if remote != "nexa:/srv/nexa/backups" {
		t.Fatalf("remote = %q", remote)
	}
}

func TestTestAccountProbesWritable(t *testing.T) {
	runner := &fakeRunner{}
	operator := &HostOperator{runner: runner, binary: "rclone"}
	result, err := operator.TestAccount(context.Background(), Account{Type: TypeLocal, Path: "/srv/backups"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("expected OK, got %+v", result)
	}
	// mkdir (write probe) must precede lsd (read probe).
	if len(runner.calls) != 2 || runner.calls[0][1] != "mkdir" || runner.calls[1][1] != "lsd" {
		t.Fatalf("unexpected rclone calls: %v", runner.calls)
	}
}

func TestRunAssemblesShipsAndPrunes(t *testing.T) {
	// lsf returns four copies incl. the new one; with a limit of 2, the two
	// oldest must be purged.
	runner := &fakeRunner{output: "2026-01-01_000000/\n2026-01-02_000000/\n2026-01-03_000000/\n2026-01-04_120000/\n"}
	operator := &HostOperator{runner: runner, binary: "rclone"}
	manifest, err := operator.Run(context.Background(), RunRequest{
		Account:     Account{Type: TypeLocal, Path: "/srv/backups"},
		PlanID:      "bkplan_1",
		CopyName:    "2026-01-04_120000",
		Limit:       2,
		Sites:       []SiteTarget{{Slug: "blog", RootPath: "/srv/nexa/sites/blog"}},
		Databases:   []DatabaseTarget{{Engine: "postgres", Name: "blog", Version: "16", Port: 5432, Socket: "/run/postgresql"}},
		StagingRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	kinds := commandKinds(runner.calls)
	// tar (site) → runuser pg_dump (db) → rclone copy → rclone lsf → 2× rclone purge
	want := []string{"tar", "runuser", "copy", "lsf", "purge", "purge"}
	if len(kinds) != len(want) {
		t.Fatalf("command sequence = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q (full: %v)", i, kinds[i], want[i], kinds)
		}
	}
	if len(manifest.Pruned) != 2 {
		t.Fatalf("expected 2 pruned, got %v", manifest.Pruned)
	}
	if manifest.RemotePath != "/srv/backups/bkplan_1/2026-01-04_120000" {
		t.Fatalf("remote path = %q", manifest.RemotePath)
	}
}

func TestRunMysqlUsesDumpTool(t *testing.T) {
	runner := &fakeRunner{}
	operator := &HostOperator{runner: runner, binary: "rclone"}
	if _, err := operator.Run(context.Background(), RunRequest{
		Account:     Account{Type: TypeLocal, Path: "/srv/backups"},
		PlanID:      "bkplan_1",
		CopyName:    "2026-01-04_120000",
		Limit:       10,
		Databases:   []DatabaseTarget{{Engine: "mariadb", Name: "shop", Socket: "/run/mysqld/mysqld.sock"}},
		StagingRoot: t.TempDir(),
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runner.calls[0][0] != "mariadb-dump" {
		t.Fatalf("expected mariadb-dump, got %v", runner.calls[0])
	}
}

func TestRestoreReplaysSiteAndDatabase(t *testing.T) {
	runner := &fakeRunner{}
	operator := &HostOperator{runner: runner, binary: "rclone"}
	err := operator.Restore(context.Background(), RestoreRequest{
		Account:  Account{Type: TypeLocal, Path: "/srv/backups"},
		PlanID:   "bkplan_1",
		CopyName: "2026-01-04_120000",
		Sites: []SiteRestoreTarget{{
			Entry: "site-blog.tar.gz", Clear: true,
			Target: SiteTarget{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"},
		}},
		Databases: []DatabaseRestoreTarget{{
			Entry: "db-postgres-blog.dump", Clear: true,
			Target: DatabaseTarget{Engine: "postgres", Name: "blog", Version: "16", Port: 5432, Socket: "/run/postgresql"},
		}},
		StagingRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// download → clear (find) → extract (tar) → reown (chown) → pg_restore (runuser)
	want := []string{"copy", "find", "tar", "chown", "runuser"}
	got := commandKinds(runner.calls)
	if len(got) != len(want) {
		t.Fatalf("command sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestCronToOnCalendar(t *testing.T) {
	cases := map[string]string{
		"0 3 * * *":    "*-*-* 3:0:00",          // daily at 03:00
		"30 2 1 * *":   "*-*-1 2:30:00",         // monthly on the 1st
		"0 0 * * 0":    "Sun *-*-* 0:0:00",      // weekly on Sunday
		"*/15 * * * *": "*-*-* *:0/15:00",       // every 15 minutes
		"0 3 * * 1-5":  "Mon..Fri *-*-* 3:0:00", // weekdays
	}
	for cron, want := range cases {
		got, err := cronToOnCalendar(cron)
		if err != nil {
			t.Fatalf("%q: %v", cron, err)
		}
		if got != want {
			t.Fatalf("cronToOnCalendar(%q) = %q, want %q", cron, got, want)
		}
	}
	if _, err := cronToOnCalendar("bad"); err == nil {
		t.Fatal("expected error for a malformed cron expression")
	}
}

func TestInstallScheduleWritesUnitsAndEnables(t *testing.T) {
	runner := &fakeRunner{}
	root := t.TempDir()
	operator := &HostOperator{runner: runner, binary: "rclone", nexaBinary: "/usr/bin/nexa", systemdRoot: root}
	if err := operator.InstallSchedule(context.Background(), ScheduleSpec{
		PlanID: "bkplan_1", PlanName: "nightly", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa-panel/control.db",
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	timer, err := os.ReadFile(filepath.Join(root, "nexa-backup-bkplan_1.timer"))
	if err != nil {
		t.Fatalf("timer not written: %v", err)
	}
	if !strings.Contains(string(timer), "OnCalendar=*-*-* 3:0:00") {
		t.Fatalf("timer missing OnCalendar: %s", timer)
	}
	service, err := os.ReadFile(filepath.Join(root, "nexa-backup-bkplan_1.service"))
	if err != nil {
		t.Fatalf("service not written: %v", err)
	}
	if !strings.Contains(string(service), "ExecStart=/usr/bin/nexa backup trigger --plan bkplan_1 --state /var/lib/nexa-panel/control.db") {
		t.Fatalf("service missing ExecStart: %s", service)
	}
	// systemctl daemon-reload then enable --now.
	if len(runner.calls) != 2 || runner.calls[0][1] != "daemon-reload" || runner.calls[1][1] != "enable" {
		t.Fatalf("systemctl calls = %v", runner.calls)
	}
}

func TestSafeEntryRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../evil", "a/b", "", ".."} {
		if _, err := safeEntry("/stage", bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	if _, err := safeEntry("/stage", "site-blog.tar.gz"); err != nil {
		t.Fatalf("legit entry rejected: %v", err)
	}
}

// commandKinds reduces each recorded call to the token that identifies it (the
// program, or the rclone sub-command for rclone calls).
func commandKinds(calls [][]string) []string {
	kinds := []string{}
	for _, call := range calls {
		if call[0] == "rclone" && len(call) > 1 {
			kinds = append(kinds, call[1])
		} else {
			kinds = append(kinds, call[0])
		}
	}
	return kinds
}

func assertEnv(t *testing.T, env []string, want string) {
	t.Helper()
	if !slices.ContainsFunc(env, func(entry string) bool { return strings.EqualFold(entry, want) }) {
		t.Fatalf("env missing %q in %v", want, env)
	}
}
