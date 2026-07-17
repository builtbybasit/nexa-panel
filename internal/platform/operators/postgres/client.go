package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
)

type UnixClient struct {
	tokenPath string
	client    *http.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &UnixClient{tokenPath: tokenPath, client: &http.Client{Transport: transport, Timeout: 30 * time.Minute}}
}

func (c *UnixClient) Discover(ctx context.Context) ([]Instance, error) {
	var result struct {
		Items []Instance `json:"items"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/postgresql/instances", nil, &result)
	return result.Items, err
}

func (c *UnixClient) Sizes(ctx context.Context, instanceID string) (map[string]int64, error) {
	var result struct {
		Sizes map[string]int64 `json:"sizes"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/postgresql/sizes?instanceId="+url.QueryEscape(instanceID), nil, &result)
	return result.Sizes, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/postgresql/plan", change, &plan)
	return plan, err
}

func (c *UnixClient) Apply(ctx context.Context, execution Execution) (Observation, error) {
	var observation Observation
	err := c.call(ctx, http.MethodPost, "/v1/postgresql/apply", execution, &observation)
	return observation, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return err
	}
	token, err := agentauth.Read(c.tokenPath)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("call PostgreSQL agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&payload)
		if payload.Message == "" {
			payload.Message = "node agent rejected PostgreSQL operation"
		}
		return errors.New(payload.Message)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(output)
}
