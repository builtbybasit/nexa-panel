package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// sizeSeparator delimits a database name from its byte count in psql's
// unaligned output. Rows are split on the last separator so a database name
// containing one cannot corrupt the count.
const sizeSeparator = "|"

// Template databases are excluded: template0 forbids connections and neither
// template is something the panel manages.
const sizeQuery = "SELECT datname, pg_database_size(datname) FROM pg_database WHERE NOT datistemplate;"

// Sizes reports the on-disk size of every database on one instance, keyed by
// database name. A single query covers the whole instance because callers
// render an entire list at once; probing one database at a time would multiply
// agent round trips by the number of rows on screen.
func (o *HostOperator) Sizes(ctx context.Context, instanceID string) (map[string]int64, error) {
	instances, err := o.Discover(ctx)
	if err != nil {
		return nil, err
	}
	instance := findInstance(instances, instanceID)
	if instance == nil {
		return nil, errors.New("PostgreSQL instance is not present on this node")
	}
	if instance.Status != "online" {
		return nil, errors.New("PostgreSQL instance is not online")
	}
	command := asPostgres(instance.Version, "psql", "--no-psqlrc", "--tuples-only", "--no-align",
		"--field-separator", sizeSeparator, "--host", o.socketRoot, "--port", strconv.Itoa(instance.Port),
		"--username", "postgres", "--dbname", "postgres", "--command", sizeQuery)
	output, err := o.runner.Run(ctx, command)
	if err != nil {
		return nil, commandError("measure PostgreSQL database sizes", output, err)
	}
	return parseSizes(string(output))
}

func findInstance(instances []Instance, id string) *Instance {
	for index := range instances {
		if instances[index].ID == id {
			return &instances[index]
		}
	}
	return nil
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
			return nil, errors.New("PostgreSQL returned an unreadable database size row")
		}
		size, err := strconv.ParseInt(strings.TrimSpace(line[separator+1:]), 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("PostgreSQL returned an unreadable database size")
		}
		sizes[strings.TrimSpace(line[:separator])] = size
	}
	return sizes, nil
}
