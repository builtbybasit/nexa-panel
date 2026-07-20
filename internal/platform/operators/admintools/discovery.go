package admintools

import (
	"context"
	"os"
	"strings"
)

func (o *HostOperator) Discover(ctx context.Context) ([]Tool, error) {
	items := Defaults()
	for index := range items {
		output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", items[index].SystemdUnit}})
		status := strings.TrimSpace(string(output))
		if err != nil || status == "" {
			status = "stopped"
		}
		items[index].Status = status
		if encoded, readErr := os.ReadFile(o.quadletPath(items[index].Kind)); readErr == nil {
			items[index] = mergeRendered(items[index], string(encoded))
		}
	}
	return items, nil
}
