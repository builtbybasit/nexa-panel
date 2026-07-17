package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sizingRunner answers discovery with one MySQL engine and hands the size
// query whatever output a test wants to put in front of the parser.
type sizingRunner struct {
	sizeOutput string
	sizeError  error
	commands   []Command
}

func (r *sizingRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "SELECT @@version") {
		return []byte("8.4.5\tMySQL Community Server - GPL\t/run/mysqld/mysqld.sock\t3306"), nil
	}
	return []byte(r.sizeOutput), r.sizeError
}

func (r *sizingRunner) sizeCommand() (Command, bool) {
	for _, command := range r.commands {
		if strings.Contains(strings.Join(command.Args, " "), "data_length") {
			return command, true
		}
	}
	return Command{}, false
}

func TestSizesReportsEverySchemaOnTheEngine(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "app_db\t491520\nwebmail\t278528\nempty_db\t0\n"}
	operator := newTestOperator(t, runner, t.TempDir())

	sizes, err := operator.Sizes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes["app_db"] != 491520 || sizes["webmail"] != 278528 {
		t.Fatalf("sizes = %+v", sizes)
	}
	// A schema holding no tables still exists and measures zero; it must not
	// read back as "never measured".
	size, measured := sizes["empty_db"]
	if !measured || size != 0 {
		t.Fatalf("empty_db size = %d, measured = %v; want 0, true", size, measured)
	}
}

// Regression: grouping information_schema.tables alone emits no row for a
// schema that holds no tables, which would leave an empty database unmeasured
// forever. Driving the query from schemata keeps it in the result.
func TestSizesQueryEnumeratesSchemataSoEmptyDatabasesAppear(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "app_db\t1\n"}
	operator := newTestOperator(t, runner, t.TempDir())

	if _, err := operator.Sizes(context.Background()); err != nil {
		t.Fatal(err)
	}
	command, found := runner.sizeCommand()
	if !found {
		t.Fatal("no size query was issued")
	}
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "information_schema.schemata") || !strings.Contains(joined, "LEFT JOIN") {
		t.Fatalf("size query does not enumerate schemata: %s", joined)
	}
	// Without these the client prints a header and aligns columns, which the
	// parser would read as garbage rows.
	for _, flag := range []string{"--batch", "--skip-column-names"} {
		if !strings.Contains(joined, flag) {
			t.Fatalf("size query missing %q: %s", flag, joined)
		}
	}
	// System schemas are noise the panel never manages.
	if !strings.Contains(joined, "performance_schema") {
		t.Fatalf("size query does not exclude system schemas: %s", joined)
	}
}

func TestSizesRejectsUnreadableOutput(t *testing.T) {
	for name, output := range map[string]string{
		"no separator":     "app_db 491520\n",
		"size not numeric": "app_db\tforty\n",
		"empty name":       "\t491520\n",
	} {
		t.Run(name, func(t *testing.T) {
			operator := newTestOperator(t, &sizingRunner{sizeOutput: output}, t.TempDir())
			if _, err := operator.Sizes(context.Background()); err == nil {
				t.Fatal("unreadable output was accepted")
			}
		})
	}
}

func TestSizesSurfacesQueryFailure(t *testing.T) {
	runner := &sizingRunner{sizeOutput: "ERROR 1049 (42000): Unknown database", sizeError: errors.New("exit status 1")}
	operator := newTestOperator(t, runner, t.TempDir())

	_, err := operator.Sizes(context.Background())
	if err == nil {
		t.Fatal("a failed size query was reported as success")
	}
	if !strings.Contains(err.Error(), "Unknown database") {
		t.Fatalf("error dropped the command output: %v", err)
	}
}
