package nodes

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
)

type UnixClient struct {
	tokenPath string
	client    *http.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &UnixClient{tokenPath: tokenPath, client: &http.Client{Transport: transport, Timeout: 8 * time.Second}}
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/node/probe/plan", change, &plan)
	return plan, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Snapshot, error) {
	var observation Snapshot
	err := c.call(ctx, http.MethodPost, "/v1/node/probe/apply", plan, &observation)
	return observation, err
}

func (c *UnixClient) Observe(ctx context.Context) (Snapshot, error) {
	var observation Snapshot
	err := c.call(ctx, http.MethodGet, "/v1/node/probe/observe", nil, &observation)
	return observation, err
}

func (c *UnixClient) Rollback(ctx context.Context, plan Plan) (Snapshot, error) {
	var observation Snapshot
	err := c.call(ctx, http.MethodPost, "/v1/node/probe/rollback", plan, &observation)
	return observation, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode agent request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return fmt.Errorf("create agent request: %w", err)
	}
	token, err := agentauth.Read(c.tokenPath)
	if err != nil {
		return fmt.Errorf("read agent credential: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("call node agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var agentError struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&agentError)
		if agentError.Message == "" {
			agentError.Message = "node agent rejected the operation"
		}
		return errors.New(agentError.Message)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(output); err != nil {
		return fmt.Errorf("decode agent response: %w", err)
	}
	return nil
}
