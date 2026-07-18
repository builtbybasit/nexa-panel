package backups

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

// UnixClient is the control-plane side of the operator: it speaks HTTP over the
// agent's Unix socket. It mirrors the pattern used by every other operator
// (see operators/postgres/client.go).
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

func (c *UnixClient) TestAccount(ctx context.Context, account Account) (TestResult, error) {
	var result TestResult
	err := c.call(ctx, http.MethodPost, "/v1/backups/accounts/test", account, &result)
	return result, err
}

func (c *UnixClient) Run(ctx context.Context, request RunRequest) (Manifest, error) {
	var manifest Manifest
	err := c.call(ctx, http.MethodPost, "/v1/backups/run", request, &manifest)
	return manifest, err
}

func (c *UnixClient) Restore(ctx context.Context, request RestoreRequest) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/restore", request, &struct{}{})
}

func (c *UnixClient) DeleteCopy(ctx context.Context, request DeleteRequest) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/copies/delete", request, &struct{}{})
}

func (c *UnixClient) InstallSchedule(ctx context.Context, spec ScheduleSpec) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/schedules/install", spec, &struct{}{})
}

func (c *UnixClient) RemoveSchedule(ctx context.Context, planID string) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/schedules/remove", map[string]string{"planId": planID}, &struct{}{})
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
		return fmt.Errorf("call backups agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&payload)
		if payload.Message == "" {
			payload.Message = "node agent rejected backup operation"
		}
		return errors.New(payload.Message)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(output)
}
