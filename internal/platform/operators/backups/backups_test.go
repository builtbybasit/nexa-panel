package backups

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records the commands rclone would have been invoked with.
type fakeRunner struct {
	calls           [][]string
	envs            [][]string
	output          string
	err             error
	downloadEntries map[string]string
	commandErrors   map[string]error
	fromFileErrors  map[int]error
	fromFileCalls   int
}

func (f *fakeRunner) Run(_ context.Context, name string, args, env []string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	if name == "tar" && len(args) >= 2 && args[0] == "-czf" {
		if err := os.WriteFile(args[1], []byte("archive"), 0o600); err != nil {
			return "", err
		}
	}
	if name == "runuser" {
		for index, argument := range args {
			if argument == "--file" && index+1 < len(args) {
				if err := os.WriteFile(args[index+1], []byte("dump"), 0o600); err != nil {
					return "", err
				}
			}
		}
	}
	if name == "rclone" && len(args) >= 3 && args[0] == "copy" && len(f.downloadEntries) != 0 && filepath.IsAbs(args[2]) {
		if err := os.MkdirAll(args[2], 0o700); err != nil {
			return "", err
		}
		for entry, content := range f.downloadEntries {
			if err := os.WriteFile(filepath.Join(args[2], entry), []byte(content), 0o600); err != nil {
				return "", err
			}
		}
	}
	command := name
	if name == "rclone" && len(args) != 0 {
		command = args[0]
	}
	if err := f.commandErrors[command]; err != nil {
		return "", err
	}
	if f.err != nil {
		return f.output, f.err
	}
	joined := strings.Join(args, " ")
	if name == "runuser" && strings.Contains(joined, "pg_get_userbyid") {
		return "postgres\n", nil
	}
	if name == "runuser" && strings.Contains(joined, "SELECT 1;") {
		return "1\n", nil
	}
	if (name == "mysql" || name == "mariadb") && strings.Contains(joined, "SELECT 1;") {
		return "1\n", nil
	}
	return f.output, f.err
}

func (f *fakeRunner) RunToFile(_ context.Context, name string, args, env []string, outPath string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	if err := os.WriteFile(outPath, []byte("dump"), 0o600); err != nil {
		return err
	}
	return f.err
}

func (f *fakeRunner) RunFromFile(_ context.Context, name string, args, env []string, inPath string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	f.fromFileCalls++
	if err := f.fromFileErrors[f.fromFileCalls]; err != nil {
		return err
	}
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
	operator := newStagedOperator(t, runner)
	manifest, err := operator.Run(context.Background(), RunRequest{
		Account:   Account{Type: TypeLocal, Path: "/srv/backups"},
		PlanID:    "bkplan_1",
		CopyName:  "2026-01-04_120000",
		Limit:     2,
		Sites:     []SiteTarget{{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"}},
		Databases: []DatabaseTarget{{Engine: "postgres", Name: "blog", Version: "16", Port: 5432, Socket: "/run/postgresql"}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	kinds := commandKinds(runner.calls)
	// Artifacts are assembled, uploaded, verified by downloading, then old
	// verified copies are pruned.
	want := []string{"tar", "runuser", "copy", "check", "lsf", "purge", "purge"}
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
	if !manifest.IntegrityChecked {
		t.Fatal("successful run did not report an integrity check")
	}
}

func TestRunMysqlUsesDumpTool(t *testing.T) {
	runner := &fakeRunner{}
	operator := newStagedOperator(t, runner)
	if _, err := operator.Run(context.Background(), RunRequest{
		Account:   Account{Type: TypeLocal, Path: "/srv/backups"},
		PlanID:    "bkplan_1",
		CopyName:  "2026-01-04_120000",
		Limit:     10,
		Databases: []DatabaseTarget{{Engine: "mariadb", Name: "shop", Socket: "/run/mysqld/mysqld.sock"}},
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runner.calls[0][0] != "mariadb-dump" {
		t.Fatalf("expected mariadb-dump, got %v", runner.calls[0])
	}
}

func TestRunPurgesUploadWhenIntegrityCheckFails(t *testing.T) {
	runner := &fakeRunner{commandErrors: map[string]error{"check": errors.New("remote data differs")}}
	operator := newStagedOperator(t, runner)
	_, err := operator.Run(context.Background(), RunRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		Sites: []SiteTarget{{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"}},
	})
	if err == nil || !strings.Contains(err.Error(), "verify uploaded backup copy") {
		t.Fatalf("Run() error = %v, want integrity failure", err)
	}
	want := []string{"tar", "copy", "check", "purge"}
	if got := commandKinds(runner.calls); !slices.Equal(got, want) {
		t.Fatalf("command sequence = %v, want %v", got, want)
	}
}

func TestRestoreReplaysSiteAndDatabase(t *testing.T) {
	archive := siteArchive(t, map[string]string{"index.html": "restored"})
	runner := &fakeRunner{downloadEntries: map[string]string{
		"site-blog.tar.gz": archive, "db-postgres-blog.dump": "dump",
	}}
	operator := newRestoreOperator(t, runner)
	documentRoot := filepath.Join(operator.siteRoot, "blog")
	if err := os.Mkdir(documentRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(documentRoot, "old.txt"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := operator.Restore(context.Background(), RestoreRequest{
		Account:  Account{Type: TypeLocal, Path: "/srv/backups"},
		PlanID:   "bkplan_1",
		CopyName: "2026-01-04_120000",
		Sites: []SiteRestoreTarget{{
			Entry: "site-blog.tar.gz", Clear: true,
			Target: SiteTarget{Slug: "blog", RootPath: documentRoot, UnixUser: "nexa_blog"},
		}},
		Databases: []DatabaseRestoreTarget{{
			Entry: "db-postgres-blog.dump", Clear: true,
			Target: DatabaseTarget{Engine: "postgres", Name: "blog", Version: "16", Port: 5432, Socket: "/run/postgresql"},
		}},
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Download and checksum verification happen before a confined in-process
	// extraction and atomic directory swap. PostgreSQL is then restored into a
	// staging database and name-swapped, so the remaining calls run as postgres.
	got := commandKinds(runner.calls)
	if len(got) < 6 || got[0] != "copy" || got[1] != "chown" {
		t.Fatalf("command sequence = %v, want copy, chown, then PostgreSQL staging/swap calls", got)
	}
	for _, kind := range got[2:] {
		if kind != "runuser" {
			t.Fatalf("unexpected command %q in database restore sequence: %v", kind, got)
		}
	}
	if content, err := os.ReadFile(filepath.Join(documentRoot, "index.html")); err != nil || string(content) != "restored" {
		t.Fatalf("restored site content = %q, err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(documentRoot, "old.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleared site retained old content: %v", err)
	}
}

func TestRestoreRejectsEscapingSiteArchiveBeforeMutation(t *testing.T) {
	archive := rawSiteArchive(t, []archiveFixture{{
		header: tar.Header{Name: "assets", Typeflag: tar.TypeSymlink, Linkname: "../../outside", Mode: 0o777},
	}})
	runner := &fakeRunner{downloadEntries: map[string]string{"site-blog.tar.gz": archive}}
	operator := newRestoreOperator(t, runner)
	documentRoot := filepath.Join(operator.siteRoot, "blog")
	if err := os.Mkdir(documentRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(documentRoot, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := operator.Restore(context.Background(), RestoreRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		Sites: []SiteRestoreTarget{{
			Entry: "site-blog.tar.gz", Clear: true,
			Target: SiteTarget{Slug: "blog", RootPath: documentRoot, UnixUser: "nexa_blog"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes the document root") {
		t.Fatalf("Restore() error = %v, want escaping-link rejection", err)
	}
	if content, readErr := os.ReadFile(marker); readErr != nil || string(content) != "keep" {
		t.Fatalf("live site was mutated: content=%q err=%v", content, readErr)
	}
	if got := commandKinds(runner.calls); !slices.Equal(got, []string{"copy"}) {
		t.Fatalf("commands after unsafe archive = %v", got)
	}
}

func TestRestoreRollsBackActivatedSiteWhenLaterDatabasePreparationFails(t *testing.T) {
	archive := siteArchive(t, map[string]string{"index.html": "restored"})
	runner := &fakeRunner{
		downloadEntries: map[string]string{"site-blog.tar.gz": archive, "db-postgres-blog.dump": "dump"},
		commandErrors:   map[string]error{"runuser": errors.New("postgres unavailable")},
	}
	operator := newRestoreOperator(t, runner)
	documentRoot := filepath.Join(operator.siteRoot, "blog")
	if err := os.Mkdir(documentRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(documentRoot, "original.txt"), []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := operator.Restore(context.Background(), RestoreRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		Sites: []SiteRestoreTarget{{
			Entry: "site-blog.tar.gz", Clear: true,
			Target: SiteTarget{Slug: "blog", RootPath: documentRoot, UnixUser: "nexa_blog"},
		}},
		Databases: []DatabaseRestoreTarget{{
			Entry:  "db-postgres-blog.dump",
			Target: DatabaseTarget{Engine: "postgres", Name: "blog", Version: "16", Port: 5432, Socket: "/run/postgresql"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "read PostgreSQL database owner") {
		t.Fatalf("Restore() error = %v", err)
	}
	if content, readErr := os.ReadFile(filepath.Join(documentRoot, "original.txt")); readErr != nil || string(content) != "original" {
		t.Fatalf("original site was not rolled back: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(documentRoot, "index.html")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed restored site remained active: %v", statErr)
	}
}

func TestMySQLRestoreFailureReplaysDurableRollbackPoint(t *testing.T) {
	runner := &fakeRunner{
		downloadEntries: map[string]string{"db-mysql-shop.sql": "new dump"},
		fromFileErrors:  map[int]error{1: errors.New("import failed")},
	}
	operator := newRestoreOperator(t, runner)
	err := operator.Restore(context.Background(), RestoreRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		Databases: []DatabaseRestoreTarget{{
			Entry:  "db-mysql-shop.sql",
			Target: DatabaseTarget{Engine: "mysql", Name: "shop", Socket: "/run/mysqld/mysqld.sock"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "import failed") {
		t.Fatalf("Restore() error = %v", err)
	}
	if runner.fromFileCalls != 3 {
		t.Fatalf("RunFromFile calls = %d, want failed import, reset, and rollback replay", runner.fromFileCalls)
	}
	recoveryEntries, readErr := os.ReadDir(filepath.Join(operator.stagingRoot, "recovery"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(recoveryEntries) != 0 {
		t.Fatalf("successful rollback left recovery artifacts: %v", recoveryEntries)
	}
}

func TestRunRejectsClientControlledStagingPathWithoutDeletingIt(t *testing.T) {
	runner := &fakeRunner{}
	operator := newStagedOperator(t, runner)
	clientRoot := t.TempDir()
	marker := filepath.Join(clientRoot, "bkplan_1", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := operator.Run(context.Background(), RunRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		StagingRoot: clientRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("Run() error = %v, want rejection of the client-controlled staging path", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("client-controlled directory was modified: %v", statErr)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none for an invalid request", runner.calls)
	}
}

func TestRestoreRejectsDestinationOutsideManagedSites(t *testing.T) {
	runner := &fakeRunner{}
	operator := newStagedOperator(t, runner)
	err := operator.Restore(context.Background(), RestoreRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		Sites: []SiteRestoreTarget{{Entry: "site-blog.tar.gz", Target: SiteTarget{Slug: "blog", RootPath: "/", UnixUser: "root"}, Clear: true}},
	})
	if err == nil || !strings.Contains(err.Error(), "managed site") {
		t.Fatalf("Restore() error = %v, want managed-site confinement rejection", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none for an invalid restore destination", runner.calls)
	}
}

func TestRestoreRejectsTamperedArtifactBeforeMutation(t *testing.T) {
	runner := &fakeRunner{downloadEntries: map[string]string{"site-blog.tar.gz": "tampered"}}
	operator := newStagedOperator(t, runner)
	err := operator.Restore(context.Background(), RestoreRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "bkplan_1", CopyName: "2026-01-04_120000",
		Sites: []SiteRestoreTarget{{
			Entry: "site-blog.tar.gz", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Target: SiteTarget{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"}, Clear: true,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("Restore() error = %v, want checksum mismatch", err)
	}
	if got := commandKinds(runner.calls); !slices.Equal(got, []string{"copy"}) {
		t.Fatalf("restore mutated a destination before verification: %v", got)
	}
}

func TestDeleteCopyRejectsPlanTraversal(t *testing.T) {
	runner := &fakeRunner{}
	operator := newStagedOperator(t, runner)
	err := operator.DeleteCopy(context.Background(), DeleteRequest{
		Account: Account{Type: TypeLocal, Path: "/srv/backups"}, PlanID: "../another-plan", CopyName: "copy",
	})
	if err == nil || !strings.Contains(err.Error(), "plan ID") {
		t.Fatalf("DeleteCopy() error = %v, want plan ID rejection", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none for an invalid delete", runner.calls)
	}
}

func TestValidateScheduleConvertsCronToOnCalendar(t *testing.T) {
	cases := map[string]string{
		"0 3 * * *":    "*-*-* 3:0:00",          // daily at 03:00
		"30 2 1 * *":   "*-*-1 2:30:00",         // monthly on the 1st
		"0 0 * * 0":    "Sun *-*-* 0:0:00",      // weekly on Sunday
		"*/15 * * * *": "*-*-* *:0/15:00",       // every 15 minutes
		"0 3 * * 1-5":  "Mon..Fri *-*-* 3:0:00", // weekdays
	}
	for cron, want := range cases {
		got, err := ValidateSchedule(cron)
		if err != nil {
			t.Fatalf("%q: %v", cron, err)
		}
		if got != want {
			t.Fatalf("ValidateSchedule(%q) = %q, want %q", cron, got, want)
		}
	}
	if _, err := ValidateSchedule("bad"); err == nil {
		t.Fatal("expected error for a malformed cron expression")
	}
	for _, invalid := range []string{
		"60 3 * * *",
		"*/0 3 * * *",
		"0 24 * * *",
		"0 3 0 * *",
		"0 3 * 13 *",
		"0 3 * * */2",
		"0 3 1 * 1",
	} {
		if _, err := ValidateSchedule(invalid); err == nil {
			t.Fatalf("ValidateSchedule(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestInstallScheduleRejectsUnitInjection(t *testing.T) {
	runner := &fakeRunner{}
	root := t.TempDir()
	operator := &HostOperator{runner: runner, binary: "rclone", nexaBinary: "/usr/bin/nexa", systemdRoot: root}
	for _, spec := range []ScheduleSpec{
		{PlanID: "bkplan_1", PlanName: "nightly\n[Service]\nExecStart=/bin/sh", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa-panel/control.db"},
		{PlanID: "../escape", PlanName: "nightly", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa-panel/control.db"},
		{PlanID: "bkplan_1", PlanName: "nightly", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa panel/control.db"},
	} {
		if err := operator.InstallSchedule(context.Background(), spec); err == nil {
			t.Fatalf("InstallSchedule(%+v) unexpectedly succeeded", spec)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("systemctl was called for rejected units: %v", runner.calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected schedules wrote files: %v", entries)
	}
}

func TestRemoveScheduleKeepsUnitsWhenDisableFails(t *testing.T) {
	runner := &fakeRunner{}
	root := t.TempDir()
	operator := &HostOperator{runner: runner, binary: "rclone", nexaBinary: "/usr/bin/nexa", systemdRoot: root}
	spec := ScheduleSpec{PlanID: "bkplan_1", PlanName: "nightly", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa-panel/control.db"}
	if err := operator.InstallSchedule(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	runner.err = os.ErrPermission
	if err := operator.RemoveSchedule(context.Background(), spec.PlanID); err == nil {
		t.Fatal("expected disable failure")
	}
	for _, suffix := range []string{".timer", ".service"} {
		if _, err := os.Stat(filepath.Join(root, unitBase(spec.PlanID)+suffix)); err != nil {
			t.Fatalf("unit %s was removed after disable failure: %v", suffix, err)
		}
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

// The control panel reinstalls every plan's schedule on each full
// reconciliation pass, so an already-converged plan must cost one status read
// and nothing else — while a unit edited outside the panel is still repaired.
func TestInstallScheduleSkipsConvergedUnitsButRepairsEditedOnes(t *testing.T) {
	runner := &fakeRunner{}
	root := t.TempDir()
	operator := &HostOperator{runner: runner, binary: "rclone", nexaBinary: "/usr/bin/nexa", systemdRoot: root}
	spec := ScheduleSpec{PlanID: "bkplan_1", PlanName: "nightly", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa-panel/control.db"}
	if err := operator.InstallSchedule(context.Background(), spec); err != nil {
		t.Fatalf("install: %v", err)
	}

	installed := len(runner.calls)
	runner.output = "ActiveState=active\nUnitFileState=enabled\n"
	if err := operator.InstallSchedule(context.Background(), spec); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	repeat := runner.calls[installed:]
	if len(repeat) != 1 || repeat[0][1] != "show" {
		t.Fatalf("converged reinstall ran %v", repeat)
	}

	timerPath := filepath.Join(root, unitBase(spec.PlanID)+".timer")
	if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=hourly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := len(runner.calls)
	if err := operator.InstallSchedule(context.Background(), spec); err != nil {
		t.Fatalf("repair: %v", err)
	}
	repair := runner.calls[edited:]
	if len(repair) != 2 || repair[0][1] != "daemon-reload" || repair[1][1] != "enable" {
		t.Fatalf("repair calls = %v", repair)
	}
	restored, err := os.ReadFile(timerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), "OnCalendar=*-*-* 3:0:00") {
		t.Fatalf("edited timer was not repaired: %s", restored)
	}
}

// A timer whose units are intact but which is stopped or disabled is not
// converged: the schedule would silently never fire.
func TestInstallScheduleReinstallsWhenTimerIsNotRunning(t *testing.T) {
	runner := &fakeRunner{}
	root := t.TempDir()
	operator := &HostOperator{runner: runner, binary: "rclone", nexaBinary: "/usr/bin/nexa", systemdRoot: root}
	spec := ScheduleSpec{PlanID: "bkplan_1", PlanName: "nightly", Cron: "0 3 * * *", StateDBPath: "/var/lib/nexa-panel/control.db"}
	if err := operator.InstallSchedule(context.Background(), spec); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, state := range []string{"ActiveState=inactive\nUnitFileState=enabled\n", "ActiveState=active\nUnitFileState=disabled\n"} {
		runner.output = state
		before := len(runner.calls)
		if err := operator.InstallSchedule(context.Background(), spec); err != nil {
			t.Fatalf("reinstall for %q: %v", state, err)
		}
		calls := runner.calls[before:]
		if len(calls) != 3 || calls[1][1] != "daemon-reload" || calls[2][1] != "enable" {
			t.Fatalf("calls for %q = %v", state, calls)
		}
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

func newStagedOperator(t *testing.T, runner Runner) *HostOperator {
	t.Helper()
	return &HostOperator{runner: runner, binary: "rclone", stagingRoot: t.TempDir()}
}

func newRestoreOperator(t *testing.T, runner Runner) *HostOperator {
	t.Helper()
	return &HostOperator{runner: runner, binary: "rclone", stagingRoot: t.TempDir(), siteRoot: t.TempDir()}
}

type archiveFixture struct {
	header tar.Header
	body   string
}

func siteArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	fixtures := make([]archiveFixture, 0, len(entries))
	for name, body := range entries {
		fixtures = append(fixtures, archiveFixture{header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(body))}, body: body})
	}
	return rawSiteArchive(t, fixtures)
}

func rawSiteArchive(t *testing.T, entries []archiveFixture) string {
	t.Helper()
	var encoded bytes.Buffer
	compressed := gzip.NewWriter(&encoded)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		if err := archive.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := archive.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.String()
}
