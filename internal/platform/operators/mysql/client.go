package mysql

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
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &UnixClient{tokenPath: tokenPath, client: &http.Client{Transport: transport, Timeout: 30 * time.Minute}}
}
func (c *UnixClient) Discover(ctx context.Context) (*Engine, error) {
	var result struct {
		Engine *Engine `json:"engine"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/mysql-family/engine", nil, &result)
	return result.Engine, err
}
func (c *UnixClient) Sizes(ctx context.Context) (map[string]int64, error) {
	var result struct {
		Sizes map[string]int64 `json:"sizes"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/mysql-family/sizes", nil, &result)
	return result.Sizes, err
}
func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var result Plan
	err := c.call(ctx, http.MethodPost, "/v1/mysql-family/plan", change, &result)
	return result, err
}
func (c *UnixClient) Apply(ctx context.Context, execution Execution) (Observation, error) {
	var result Observation
	err := c.call(ctx, http.MethodPost, "/v1/mysql-family/apply", execution, &result)
	return result, err
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
		return fmt.Errorf("call MySQL-family agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&payload)
		if payload.Message == "" {
			payload.Message = "node agent rejected MySQL-family operation"
		}
		return errors.New(payload.Message)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(output)
}
