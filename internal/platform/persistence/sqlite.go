package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"
)

// Open creates the small, local control-plane database. Feature modules declare
// their schemas as timestamped SQL files applied centrally by RunMigrations.
func Open(path string) (*bun.DB, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	sqlDatabase, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// A single writer is enough for this low-traffic control plane and avoids
	// lock contention while keeping connection overhead predictable.
	sqlDatabase.SetMaxOpenConns(1)
	sqlDatabase.SetMaxIdleConns(1)
	sqlDatabase.SetConnMaxLifetime(0)
	database := bun.NewDB(sqlDatabase, sqlitedialect.New())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	// The ping is what actually creates the file, so the group fix belongs here
	// and not before it. It is best-effort: a control plane that can talk to its
	// database must not refuse to boot over a cosmetic group, and every
	// deployment where the fix matters also has tmpfiles as a second chance.
	_ = alignStateGroup(absolute, ownerGroup(absolute))
	return database, nil
}

// ownerGroup is the login group of the account that OWNS the state file, which
// is the group the packaged tmpfiles snippet declares for it. It is read from
// the file rather than from the running process because Open runs under two
// different identities. The packaged control plane runs as User=nexa with
// Group=www-data so that Nginx can connect to the API socket, so everything it
// creates inherits www-data — the drift this fixes. The root recovery CLIs
// (nexa mfa reset, nexa audit verify, nexa backup system) open the same
// database as root, and aligning to the running process there would replace one
// contract violation with another by handing the file to group root. Both cases
// resolve to the owner's own group, which is what the contract asks for.
// Returns -1 when the owner cannot be resolved, which alignStateGroup treats as
// "leave it alone".
func ownerGroup(path string) int {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	account, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10))
	if err != nil {
		return -1
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return -1
	}
	return gid
}

// alignStateGroup gives the control database and its SQLite side files back to
// the owning account's own group. The web-facing group must not own the file
// holding every panel secret: the mode is 0600 today, but an ownership that
// contradicts the packaged tmpfiles contract is drift, and drift is what turns
// one future mode change into a disclosure. The account owns the files and
// belongs to the target group, so this needs no privilege.
func alignStateGroup(path string, gid int) error {
	if gid < 0 {
		return nil
	}
	var failure error
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && int(stat.Gid) == gid {
			continue
		}
		if err := os.Chown(name, -1, gid); err != nil && failure == nil {
			failure = fmt.Errorf("align group of %s: %w", name, err)
		}
	}
	return failure
}
