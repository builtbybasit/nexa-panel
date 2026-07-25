package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type diagnosticsRequest struct {
	DelayMilliseconds int `json:"delayMilliseconds"`
}

func diagnosticsHandler(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var input diagnosticsRequest
	if err := json.Unmarshal(request, &input); err != nil {
		return nil, errors.New("invalid diagnostics request")
	}
	delay := time.Duration(input.DelayMilliseconds) * time.Millisecond
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	if delay > 2*time.Second {
		return nil, errors.New("diagnostics delay exceeds two seconds")
	}
	steps := []struct {
		progress int
		message  string
	}{
		{20, "Checking control-panel persistence."},
		{50, "Checking durable progress events."},
		{80, "Checking worker cancellation."},
	}
	for _, step := range steps {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if err := report(step.progress, step.message); err != nil {
			return nil, err
		}
	}
	return map[string]any{"healthy": true, "checks": len(steps)}, nil
}
