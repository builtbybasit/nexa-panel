// Package agentclient owns the authenticated HTTP-over-Unix-socket transport
// shared by control-panel operator adapters. Feature packages only describe
// their request and response types; token rotation, bounded JSON decoding, and
// agent error handling stay consistent in this one transport seam.
package agentclient

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

const (
	defaultResponseLimit = int64(1024 * 1024)
	errorResponseLimit   = int64(16 * 1024)
)

// ResponseError is the safe error envelope returned by the privileged agent.
// Keeping the status and machine code makes transport failures observable while
// Error remains suitable for existing operator interfaces.
type ResponseError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
}

func (e *ResponseError) Error() string { return e.Message }

type tokenSource func() (string, error)

type Client struct {
	http          *http.Client
	token         tokenSource
	service       string
	fallbackError string
	responseLimit int64
}

type Option func(*Client)

func WithResponseLimit(bytes int64) Option {
	return func(client *Client) {
		if bytes > 0 {
			client.responseLimit = bytes
		}
	}
}

// New creates an authenticated JSON client over socketPath. The token file is
// read for every request so credential rotation never requires a process restart.
func New(socketPath, tokenPath, service, fallbackError string, timeout time.Duration, options ...Option) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := newClient(
		&http.Client{Transport: transport, Timeout: timeout},
		func() (string, error) { return agentauth.Read(tokenPath) },
		service,
		fallbackError,
	)
	for _, option := range options {
		option(client)
	}
	return client
}

func newClient(httpClient *http.Client, token tokenSource, service, fallbackError string) *Client {
	if service == "" {
		service = "node"
	}
	if fallbackError == "" {
		fallbackError = "node agent rejected the operation"
	}
	return &Client{
		http: httpClient, token: token, service: service,
		fallbackError: fallbackError, responseLimit: defaultResponseLimit,
	}
}

// JSON performs one authenticated JSON request. Nil input sends no body; nil
// output and 204 responses are valid and do not attempt to decode an empty body.
func (c *Client) JSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode %s agent request: %w", c.service, err)
		}
		body = bytes.NewReader(encoded)
	}

	contentType := ""
	if input != nil {
		contentType = "application/json"
	}
	response, err := c.Do(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return c.responseError(response)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, c.responseLimit)).Decode(output); err != nil {
		return fmt.Errorf("decode %s agent response: %w", c.service, err)
	}
	return nil
}

// Do performs an authenticated request while leaving response-body ownership
// and status decoding to the caller. It is the narrow escape hatch for file and
// log streaming; ordinary operator calls should use JSON.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return nil, fmt.Errorf("create %s agent request: %w", c.service, err)
	}
	token, err := c.token()
	if err != nil {
		return nil, fmt.Errorf("read %s agent credential: %w", c.service, err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call %s agent: %w", c.service, err)
	}
	return response, nil
}

func (c *Client) responseError(response *http.Response) error {
	payload := &ResponseError{StatusCode: response.StatusCode}
	if err := json.NewDecoder(io.LimitReader(response.Body, errorResponseLimit)).Decode(payload); err != nil && !errors.Is(err, io.EOF) {
		payload.Message = ""
	}
	if payload.Message == "" {
		payload.Message = c.fallbackError
	}
	return payload
}
