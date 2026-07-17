package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sizingRunner answers discovery with one online instance and hands the size
// query whatever output a test wants to put in front of the parser.
type sizingRunner struct {
	sizeOutput string
	sizeError  error
	commands   []Command
}

func (r *sizingRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if command.Name == "pg_lsclusters" {
		return []byte(`[{"version":"18","cluster":"nexa_main","port":5432,"status":"online","owner":"postgres"}]`), nil
	}
	return []byte(r.sizeOutput), r.sizeError
}

func (r *sizingRunner) sizeCommand() (Command, bool) {
	for _, command := range r.commands {
		if strings.Contains(strings.Join(command.Args, " "), "pg_database_size") {
			return command, true
		}
	}
	return Command{}, false
}

func TestSizesReportsEveryDatabaseOnTheInstance(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "app_db|491520\npostgres|8331264\nempty_db|0\n"}
	operator := newTestOperator(t, runner)

	sizes, err := operator.Sizes(context.Background(), "postgresql_18_nexa_main")
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes["app_db"] != 491520 || sizes["postgres"] != 8331264 {
		t.Fatalf("sizes = %+v", sizes)
	}
	// A database that measures zero must be reported as zero rather than
	// dropped, so callers can tell "empty" from "never measured".
	size, measured := sizes["empty_db"]
	if !measured || size != 0 {
		t.Fatalf("empty_db size = %d, measured = %v; want 0, true", size, measured)
	}
}

func TestSizesQueriesOneInstanceWithUnalignedTupleOutput(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "app_db|1\n"}
	operator := newTestOperator(t, runner)

	if _, err := operator.Sizes(context.Background(), "postgresql_18_nexa_main"); err != nil {
		t.Fatal(err)
	}
	command, found := runner.sizeCommand()
	if !found {
		t.Fatal("no pg_database_size query was issued")
	}
	joined := strings.Join(command.Args, " ")
	// Without both flags psql prints a header and pads columns, which the
	// parser would read as garbage rows.
	for _, flag := range []string{"--tuples-only", "--no-align", "--port 5432", "/18/bin/psql"} {
		if !strings.Contains(joined, flag) {
			t.Fatalf("size query missing %q: %s", flag, joined)
		}
	}
	if command.Name != "runuser" {
		t.Fatalf("size query ran as %q, want it de-escalated through runuser", command.Name)
	}
	// Template databases are excluded: template0 refuses connections.
	if !strings.Contains(joined, "NOT datistemplate") {
		t.Fatalf("size query does not exclude template databases: %s", joined)
	}
}

// Regression: database names are not restricted to the panel's own naming
// pattern, so a name containing the field separator must not shift the parse
// and corrupt the byte count.
func TestSizesParsesNamesContainingTheFieldSeparator(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "od|dly|named|4096\n"}
	operator := newTestOperator(t, runner)

	sizes, err := operator.Sizes(context.Background(), "postgresql_18_nexa_main")
	if err != nil {
		t.Fatal(err)
	}
	if sizes["od|dly|named"] != 4096 {
		t.Fatalf("sizes = %+v", sizes)
	}
}

func TestSizesRejectsUnreadableOutput(t *testing.T) {
	for name, output := range map[string]string{
		"no separator":     "app_db 491520\n",
		"size not numeric": "app_db|forty\n",
		"empty name":       "|491520\n",
	} {
		t.Run(name, func(t *testing.T) {
			operator := newTestOperator(t, &sizingRunner{sizeOutput: output})
			if _, err := operator.Sizes(context.Background(), "postgresql_18_nexa_main"); err == nil {
				t.Fatal("unreadable output was accepted")
			}
		})
	}
}

func TestSizesFailsForAnInstanceThatIsNotPresent(t *testing.T) {
	operator := newTestOperator(t, &sizingRunner{sizeOutput: "app_db|1\n"})
	if _, err := operator.Sizes(context.Background(), "postgresql_16_missing"); err == nil {
		t.Fatal("sizes were reported for an instance that is not on this node")
	}
}

func TestSizesSurfacesQueryFailure(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "FATAL: the database system is starting up", sizeError: errors.New("exit status 2")}
	operator := newTestOperator(t, runner)

	_, err := operator.Sizes(context.Background(), "postgresql_18_nexa_main")
	if err == nil {
		t.Fatal("a failed size query was reported as success")
	}
	// The psql output carries the reason; losing it would leave the caller
	// with a bare exit status.
	if !strings.Contains(err.Error(), "starting up") {
		t.Fatalf("error dropped the command output: %v", err)
	}
}
