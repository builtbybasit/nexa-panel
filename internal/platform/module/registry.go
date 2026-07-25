package module

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

type Descriptor struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Version              string   `json:"version"`
	Description          string   `json:"description"`
	Dependencies         []string `json:"dependencies"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
	EstimatedIdleBytes   uint64   `json:"estimatedIdleBytes"`
}

type Registry interface {
	Handle(pattern string, handler http.Handler) error
	HandleAuthenticated(pattern string, handler http.Handler) error
	HandleAuthorized(pattern, permission string, handler http.Handler) error
}

type Module interface {
	Descriptor() Descriptor
	Register(registry Registry) error
}

// Background is implemented by modules that own workers or reconciliation
// loops. The control panel starts them only after the complete dependency graph
// and route registry have validated, then closes them in reverse dependency
// order during shutdown.
type Background interface {
	Start(context.Context)
	Close()
}

func ValidateAndSort(modules []Module) ([]Module, error) {
	byID := make(map[string]Module, len(modules))
	for _, candidate := range modules {
		if candidate == nil {
			return nil, errors.New("module cannot be nil")
		}
		descriptor := candidate.Descriptor()
		if descriptor.ID == "" || descriptor.Name == "" || descriptor.Version == "" {
			return nil, fmt.Errorf("module descriptor requires id, name, and version")
		}
		if _, exists := byID[descriptor.ID]; exists {
			return nil, fmt.Errorf("duplicate module id %q", descriptor.ID)
		}
		byID[descriptor.ID] = candidate
	}

	for id, candidate := range byID {
		for _, dependency := range candidate.Descriptor().Dependencies {
			if _, exists := byID[dependency]; !exists {
				return nil, fmt.Errorf("module %q requires missing module %q", id, dependency)
			}
		}
	}

	ordered := make([]Module, 0, len(modules))
	resolved := make(map[string]struct{}, len(modules))
	remaining := make(map[string]Module, len(byID))
	for id, candidate := range byID {
		remaining[id] = candidate
	}

	for len(remaining) > 0 {
		ready := make([]string, 0, len(remaining))
		for id, candidate := range remaining {
			dependenciesReady := true
			for _, dependency := range candidate.Descriptor().Dependencies {
				if _, ok := resolved[dependency]; !ok {
					dependenciesReady = false
					break
				}
			}
			if dependenciesReady {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return nil, errors.New("module dependency cycle detected")
		}
		slices.Sort(ready)
		for _, id := range ready {
			ordered = append(ordered, remaining[id])
			resolved[id] = struct{}{}
			delete(remaining, id)
		}
	}
	return ordered, nil
}
