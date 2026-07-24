package mysql

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
)

// The client runs with --batch, so rows arrive tab-separated with special
// characters escaped. Rows are split on the last tab so a schema name cannot
// corrupt the count.
const sizeSeparator = "\t"

// Driven from schemata rather than grouping tables directly: an empty schema
// has no rows in information_schema.tables, and grouping alone would drop it
// from the result entirely, leaving callers unable to tell "empty" from
// "no longer exists". The left join gives every live schema a row — zero when
// it holds nothing — which matches how pg_database enumerates on the
// PostgreSQL side. System schemas are excluded as the panel never manages them.
//
// Unlike pg_database_size, this measures table data plus indexes rather than
// everything on disk, and InnoDB reports those lengths approximately.
const sizeQuery = "SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0) " +
	"FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name " +
	"WHERE s.schema_name NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys') GROUP BY s.schema_name;"

// Sizes reports the size of every non-system schema, keyed by name. A schema
// absent from the result no longer exists on the host.
func (o *HostOperator) Sizes(ctx context.Context) (map[string]int64, error) {
	engine, err := o.Discover(ctx)
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, errors.New("no MySQL-family engine is present on this node")
	}
	output, err := o.runner.Run(ctx, o.clientCommand(engine, sizeQuery))
	if err != nil {
		return nil, hostcmd.Error("measure MySQL-family database sizes", output, err)
	}
	return parseSizes(string(output))
}

func parseSizes(output string) (map[string]int64, error) {
	sizes := map[string]int64{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		separator := strings.LastIndex(line, sizeSeparator)
		if separator < 1 {
			return nil, errors.New("MySQL-family returned an unreadable database size row")
		}
		size, err := strconv.ParseInt(strings.TrimSpace(line[separator+1:]), 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("MySQL-family returned an unreadable database size")
		}
		sizes[strings.TrimSpace(line[:separator])] = size
	}
	return sizes, nil
}
