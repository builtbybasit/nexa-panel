package mysql

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

func (o *HostOperator) Discover(ctx context.Context) (*Engine, error) {
	command := o.clientCommandName("mysql", "SELECT @@version, @@version_comment, @@socket, @@port;")
	output, err := o.runner.Run(ctx, command)
	if err != nil {
		if !commandMissing(output, err) {
			return nil, commandError("discover MySQL-family engine", output, err)
		}
		command = o.clientCommandName("mariadb", "SELECT @@version, @@version_comment, @@socket, @@port;")
		output, err = o.runner.Run(ctx, command)
		if err != nil {
			if commandMissing(output, err) {
				return nil, nil
			}
			return nil, commandError("discover MySQL-family engine", output, err)
		}
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) != 4 {
		return nil, errors.New("MySQL-family discovery returned an invalid response")
	}
	port, err := strconv.Atoi(parts[3])
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("MySQL-family discovery returned an invalid port")
	}
	versionText := strings.TrimSpace(parts[0] + " " + parts[1])
	kind := EngineMySQL
	unit := "mysql.service"
	if strings.Contains(strings.ToLower(versionText), "mariadb") {
		kind, unit = EngineMariaDB, "mariadb.service"
	}
	version := strings.TrimSpace(parts[0])
	return &Engine{ID: string(kind), Kind: kind, Version: version, VersionText: versionText, SocketPath: filepath.Clean(parts[2]), Port: port, Status: "online", SystemdUnit: unit}, nil
}
