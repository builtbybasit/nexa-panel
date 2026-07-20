package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

func runDoctor(logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	memory, memoryErr := capacity.NewProcReader().Read(ctx)
	containerRuntime, podmanErr := podman.NewInspector().Inspect(ctx)

	fmt.Printf("Nexa Panel %s\n", version.Version)
	if memoryErr != nil {
		fmt.Printf("Memory: unavailable (%v)\n", memoryErr)
	} else {
		fmt.Printf(
			"Memory: %s total, %s available, profile %s\n",
			capacity.FormatBytes(memory.TotalBytes),
			capacity.FormatBytes(memory.AvailableBytes),
			memory.Profile,
		)
	}

	if podmanErr != nil {
		fmt.Printf("Podman: unavailable (%v)\n", podmanErr)
	} else {
		fmt.Printf("Podman: %s at %s\n", containerRuntime.Version, containerRuntime.Path)
	}

	logger.Info("doctor completed")
	return nil
}
