package sites

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
	return &UnixClient{tokenPath: tokenPath, client: &http.Client{Transport: transport, Timeout: 2 * time.Minute}}
}

func (c *UnixClient) Plan(ctx context.Context, site Site) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/sites/plan", site, &plan)
	return plan, err
}
func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Observation, error) {
	var observation Observation
	err := c.call(ctx, http.MethodPost, "/v1/sites/apply", plan, &observation)
	return observation, err
}
func (c *UnixClient) Rollback(ctx context.Context, plan Plan) (Observation, error) {
	var observation Observation
	err := c.call(ctx, http.MethodPost, "/v1/sites/rollback", plan, &observation)
	return observation, err
}
func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	token, err := agentauth.Read(c.tokenPath)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("call site agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&payload)
		if payload.Message == "" {
			payload.Message = "node agent rejected the site operation"
		}
		return errors.New(payload.Message)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(output)
}
