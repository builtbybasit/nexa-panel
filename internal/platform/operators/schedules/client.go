package schedules

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
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
	json *agentclient.Client
	// Manual runs last up to the task timeout; they are bounded by the
	// caller's context, not a wall-clock timeout every long task would hit.
	run *agentclient.Client
}

var _ Operator = (*UnixClient)(nil)

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{
		json: agentclient.New(socketPath, tokenPath, "schedules", "node agent rejected the schedule operation", 2*time.Minute, agentclient.WithResponseLimit(8*1024*1024)),
		run:  agentclient.New(socketPath, tokenPath, "schedules", "node agent rejected the schedule operation", 0, agentclient.WithResponseLimit(8*1024*1024)),
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

func (c *UnixClient) call(ctx context.Context, client *agentclient.Client, path string, input, output any) error {
	err := client.JSON(ctx, http.MethodPost, path, input, output)
	var responseError *agentclient.ResponseError
	if errors.As(err, &responseError) && responseError.Code != "" && StatusFor(responseError.Code) != http.StatusInternalServerError {
		return &OperationError{Code: responseError.Code, Message: responseError.Message}
	}
	return err
}
