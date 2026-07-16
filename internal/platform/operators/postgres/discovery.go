package postgres

import (
	"context"
	"os/exec"

	"errors"
	"fmt"

	"path/filepath"

	"encoding/json"
	"sort"
)

func (o *HostOperator) Discover(ctx context.Context) ([]Instance, error) {
	output, err := o.runner.Run(ctx, Command{Name: "pg_lsclusters", Args: []string{"--json"}})
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return []Instance{}, nil
		}
		return nil, commandError("discover PostgreSQL instances", output, err)
	}
	var rows []struct {
		Version       jsonString      `json:"version"`
		Cluster       string          `json:"cluster"`
		Name          string          `json:"name"`
		Port          jsonInt         `json:"port"`
		Status        string          `json:"status"`
		Running       json.RawMessage `json:"running"`
		Owner         string          `json:"owner"`
		OwnerUID      json.RawMessage `json:"owneruid"`
		DataDirectory string          `json:"data_directory"`
		DataPath      string          `json:"data_path"`
		PGData        string          `json:"pgdata"`
		LogFile       string          `json:"log_file"`
		LogPath       string          `json:"log_path"`
		Logfile       string          `json:"logfile"`
		SocketDir     string          `json:"socketdir"`
	}
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, errors.New("pg_lsclusters returned invalid JSON")
	}
	items := make([]Instance, 0, len(rows))
	for _, row := range rows {
		cluster := first(row.Cluster, row.Name)
		dataPath := first(row.DataDirectory, row.DataPath, row.PGData)
		logPath := first(row.LogFile, row.LogPath, row.Logfile)
		version, port := string(row.Version), int(row.Port)
		if !supportedVersion(version) || !clusterPattern.MatchString(cluster) || port < 1 || port > 65535 {
			continue
		}
		if dataPath == "" {
			dataPath = filepath.Join(o.dataRoot, version, cluster)
		}
		if logPath == "" {
			logPath = filepath.Join(o.logRoot, fmt.Sprintf("postgresql-%s-%s.log", version, cluster))
		}
		status := normalizeStatus(row.Status)
		if row.Status == "" && jsonTruthy(row.Running) {
			status = "online"
		}
		item := o.instance(version, cluster, port, status, first(row.Owner, ownerFromUID(row.OwnerUID), "postgres"), dataPath, logPath)
		item.SocketPath = first(row.SocketDir, o.socketRoot)
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Version == items[j].Version {
			return items[i].Cluster < items[j].Cluster
		}
		return items[i].Version < items[j].Version
	})
	return items, nil
}
