package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

type ListRequest struct {
	Scope sitefs.Scope `json:"scope"`
}

type ReadCall struct {
	Scope   sitefs.Scope `json:"scope"`
	Request ReadRequest  `json:"request"`
}

type UnixClient struct {
	json   *agentclient.Client
	stream *agentclient.Client
}

var _ Operator = (*UnixClient)(nil)

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{
		json: agentclient.New(socketPath, tokenPath, "logs", "node agent rejected the log operation", 2*time.Minute, agentclient.WithResponseLimit(8*1024*1024)),
		// Streaming downloads are bounded by the caller's context, not a
		// wall-clock timeout that a large log would always exceed.
		stream: agentclient.New(socketPath, tokenPath, "logs", "node agent rejected the log operation", 0),
	}
}

func (c *UnixClient) List(ctx context.Context, scope sitefs.Scope) (Listing, error) {
	var listing Listing
	err := c.call(ctx, "/v1/logs/list", ListRequest{Scope: scope}, &listing)
	return listing, err
}

func (c *UnixClient) Read(ctx context.Context, scope sitefs.Scope, request ReadRequest) (ReadResult, error) {
	var result ReadResult
	err := c.call(ctx, "/v1/logs/read", ReadCall{Scope: scope, Request: request}, &result)
	return result, err
}

func (c *UnixClient) Download(ctx context.Context, scope sitefs.Scope, name string) (io.ReadCloser, Entry, error) {
	values := url.Values{"name": []string{name}}
	response, err := c.stream.Do(ctx, http.MethodGet, "/v1/logs/download?"+scopeQuery(scope, values), nil, "")
	if err != nil {
		return nil, Entry{}, fmt.Errorf("call logs agent: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, Entry{}, decodeFailure(response)
	}
	var entry Entry
	if err := json.Unmarshal([]byte(response.Header.Get("X-Nexa-Entry")), &entry); err != nil {
		response.Body.Close()
		return nil, Entry{}, errors.New("node agent download response is missing entry metadata")
	}
	return response.Body, entry, nil
}

func (c *UnixClient) call(ctx context.Context, path string, input, output any) error {
	err := c.json.JSON(ctx, http.MethodPost, path, input, output)
	var responseError *agentclient.ResponseError
	if errors.As(err, &responseError) && responseError.Code != "" && StatusFor(responseError.Code) != http.StatusInternalServerError {
		return &OperationError{Code: responseError.Code, Message: responseError.Message}
	}
	return err
}

func scopeQuery(scope sitefs.Scope, values url.Values) string {
	if values == nil {
		values = url.Values{}
	}
	values.Set("site", scope.SiteID)
	values.Set("slug", scope.Slug)
	values.Set("root", scope.RootPath)
	values.Set("user", scope.UnixUser)
	return values.Encode()
}

func decodeFailure(response *http.Response) error {
	var payload OperationError
	_ = json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&payload)
	if payload.Code != "" && StatusFor(payload.Code) != http.StatusInternalServerError {
		return &payload
	}
	if payload.Message == "" {
		payload.Message = "node agent rejected the log operation"
	}
	return errors.New(payload.Message)
}
