package schedules

import (
	"bytes"
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"io"
	"net"
	"net/http"

	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

type PlanRequest struct {
	Task    Task `json:"task"`
	Removal bool `json:"removal"`
}

type RunRequest struct {
	Task Task `json:"task"`
}

type RunsRequest struct {
	Scope  sitefs.Scope `json:"scope"`
	TaskID string       `json:"taskId"`
}

type RunsResult struct {
	Items []RunRecord `json:"items"`
}

type UnixClient struct {
	tokenPath string
	json      *http.Client
	// Manual runs last up to the task timeout; they are bounded by the
	// caller's context, not a wall-clock timeout every long task would hit.
	run *http.Client
}

var _ Operator = (*UnixClient)(nil)

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &UnixClient{
		tokenPath: tokenPath,
		json:      &http.Client{Transport: transport, Timeout: 2 * time.Minute},
		run:       &http.Client{Transport: transport},
	}
}

func (c *UnixClient) Plan(ctx context.Context, task Task, removal bool) (Plan, error) {
	var plan Plan
	err := c.call(ctx, c.json, "/v1/schedules/plan", PlanRequest{Task: task, Removal: removal}, &plan)
	return plan, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Observation, error) {
	var observation Observation
	err := c.call(ctx, c.json, "/v1/schedules/apply", plan, &observation)
	return observation, err
}

func (c *UnixClient) Rollback(ctx context.Context, plan Plan) (Observation, error) {
	var observation Observation
	err := c.call(ctx, c.json, "/v1/schedules/rollback", plan, &observation)
	return observation, err
}

func (c *UnixClient) Run(ctx context.Context, task Task) (RunResult, error) {
	var result RunResult
	err := c.call(ctx, c.run, "/v1/schedules/run", RunRequest{Task: task}, &result)
	return result, err
}

func (c *UnixClient) Runs(ctx context.Context, scope sitefs.Scope, taskID string) ([]RunRecord, error) {
	var result RunsResult
	if err := c.call(ctx, c.json, "/v1/schedules/runs", RunsRequest{Scope: scope, TaskID: taskID}, &result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c *UnixClient) call(ctx context.Context, client *http.Client, path string, input, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	token, err := agentauth.Read(c.tokenPath)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call schedules agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeFailure(response)
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8*1024*1024)).Decode(output)
}

func decodeFailure(response *http.Response) error {
	var payload OperationError
	_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&payload)
	if payload.Code != "" && StatusFor(payload.Code) != http.StatusInternalServerError {
		return &payload
	}
	if payload.Message == "" {
		payload.Message = "node agent rejected the schedule operation"
	}
	return errors.New(payload.Message)
}
