package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

type PathRequest struct {
	Scope sitefs.Scope `json:"scope"`
	Path  string       `json:"path"`
}

type WriteRequest struct {
	Scope        sitefs.Scope `json:"scope"`
	Path         string       `json:"path"`
	Content      string       `json:"content"`
	ExpectedETag string       `json:"expectedEtag"`
}

type MoveRequest struct {
	Scope     sitefs.Scope `json:"scope"`
	From      string       `json:"from"`
	To        string       `json:"to"`
	Overwrite bool         `json:"overwrite"`
}

type ChmodRequest struct {
	Scope sitefs.Scope `json:"scope"`
	Path  string       `json:"path"`
	Mode  string       `json:"mode"`
}

type CopyRequest struct {
	Scope sitefs.Scope `json:"scope"`
	From  string       `json:"from"`
	To    string       `json:"to"`
}

type DeleteRequest struct {
	Scope     sitefs.Scope `json:"scope"`
	Path      string       `json:"path"`
	Recursive bool         `json:"recursive"`
}

type UploadBeginRequest struct {
	Scope     sitefs.Scope `json:"scope"`
	Path      string       `json:"path"`
	Size      int64        `json:"size"`
	Overwrite bool         `json:"overwrite"`
}

type UploadScopeRequest struct {
	Scope sitefs.Scope `json:"scope"`
}

type ArchiveRequest struct {
	Scope  sitefs.Scope `json:"scope"`
	Paths  []string     `json:"paths"`
	Target string       `json:"target"`
}

type ExtractRequest struct {
	Scope     sitefs.Scope `json:"scope"`
	Path      string       `json:"path"`
	TargetDir string       `json:"targetDir"`
}

type UnixClient struct {
	json   *agentclient.Client
	stream *agentclient.Client
}

var _ Operator = (*UnixClient)(nil)

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{
		json: agentclient.New(socketPath, tokenPath, "files", "node agent rejected the file operation", 2*time.Minute, agentclient.WithResponseLimit(8*1024*1024)),
		// Streaming transfers are bounded by the caller's context, not a
		// wall-clock timeout that a large file would always exceed.
		stream: agentclient.New(socketPath, tokenPath, "files", "node agent rejected the file operation", 0),
	}
}

func (c *UnixClient) List(ctx context.Context, scope sitefs.Scope, path string) (Listing, error) {
	var listing Listing
	err := c.call(ctx, "/v1/files/list", PathRequest{Scope: scope, Path: path}, &listing)
	return listing, err
}

func (c *UnixClient) Stat(ctx context.Context, scope sitefs.Scope, path string) (Entry, error) {
	var entry Entry
	err := c.call(ctx, "/v1/files/stat", PathRequest{Scope: scope, Path: path}, &entry)
	return entry, err
}

func (c *UnixClient) Read(ctx context.Context, scope sitefs.Scope, path string) (ReadResult, error) {
	var result ReadResult
	err := c.call(ctx, "/v1/files/read", PathRequest{Scope: scope, Path: path}, &result)
	return result, err
}

func (c *UnixClient) Write(ctx context.Context, scope sitefs.Scope, path, content, expectedETag string) (WriteResult, error) {
	var result WriteResult
	err := c.call(ctx, "/v1/files/write", WriteRequest{Scope: scope, Path: path, Content: content, ExpectedETag: expectedETag}, &result)
	return result, err
}

func (c *UnixClient) Mkdir(ctx context.Context, scope sitefs.Scope, path string) (Entry, error) {
	var entry Entry
	err := c.call(ctx, "/v1/files/mkdir", PathRequest{Scope: scope, Path: path}, &entry)
	return entry, err
}

func (c *UnixClient) Move(ctx context.Context, scope sitefs.Scope, from, to string, overwrite bool) (Entry, error) {
	var entry Entry
	err := c.call(ctx, "/v1/files/move", MoveRequest{Scope: scope, From: from, To: to, Overwrite: overwrite}, &entry)
	return entry, err
}

func (c *UnixClient) Chmod(ctx context.Context, scope sitefs.Scope, path, mode string) (Entry, error) {
	var entry Entry
	err := c.call(ctx, "/v1/files/chmod", ChmodRequest{Scope: scope, Path: path, Mode: mode}, &entry)
	return entry, err
}

func (c *UnixClient) Copy(ctx context.Context, scope sitefs.Scope, from, to string) (CopyResult, error) {
	var result CopyResult
	err := c.call(ctx, "/v1/files/copy", CopyRequest{Scope: scope, From: from, To: to}, &result)
	return result, err
}

func (c *UnixClient) Delete(ctx context.Context, scope sitefs.Scope, path string, recursive bool) error {
	return c.call(ctx, "/v1/files/delete", DeleteRequest{Scope: scope, Path: path, Recursive: recursive}, nil)
}

func (c *UnixClient) UploadBegin(ctx context.Context, scope sitefs.Scope, path string, size int64, overwrite bool) (Upload, error) {
	var upload Upload
	err := c.call(ctx, "/v1/files/uploads", UploadBeginRequest{Scope: scope, Path: path, Size: size, Overwrite: overwrite}, &upload)
	return upload, err
}

func (c *UnixClient) UploadChunk(ctx context.Context, scope sitefs.Scope, uploadID string, offset int64, body io.Reader) error {
	values := url.Values{"offset": []string{strconv.FormatInt(offset, 10)}}
	response, err := c.stream.Do(ctx, http.MethodPut, "/v1/files/uploads/"+url.PathEscape(uploadID)+"?"+scopeQuery(scope, values), body, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("call files agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeFailure(response)
	}
	return nil
}

func (c *UnixClient) UploadCommit(ctx context.Context, scope sitefs.Scope, uploadID string) (Entry, error) {
	var entry Entry
	err := c.call(ctx, "/v1/files/uploads/"+url.PathEscape(uploadID)+"/commit", UploadScopeRequest{Scope: scope}, &entry)
	return entry, err
}

func (c *UnixClient) UploadAbort(ctx context.Context, scope sitefs.Scope, uploadID string) error {
	response, err := c.json.Do(ctx, http.MethodDelete, "/v1/files/uploads/"+url.PathEscape(uploadID)+"?"+scopeQuery(scope, nil), nil, "")
	if err != nil {
		return fmt.Errorf("call files agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeFailure(response)
	}
	return nil
}

func (c *UnixClient) Download(ctx context.Context, scope sitefs.Scope, path string) (io.ReadCloser, Entry, error) {
	values := url.Values{"path": []string{path}}
	response, err := c.stream.Do(ctx, http.MethodGet, "/v1/files/download?"+scopeQuery(scope, values), nil, "")
	if err != nil {
		return nil, Entry{}, fmt.Errorf("call files agent: %w", err)
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

func (c *UnixClient) Archive(ctx context.Context, scope sitefs.Scope, paths []string, target string) (ArchiveResult, error) {
	var result ArchiveResult
	err := c.call(ctx, "/v1/files/archive", ArchiveRequest{Scope: scope, Paths: paths, Target: target}, &result)
	return result, err
}

func (c *UnixClient) Extract(ctx context.Context, scope sitefs.Scope, path, targetDir string) (ExtractResult, error) {
	var result ExtractResult
	err := c.call(ctx, "/v1/files/extract", ExtractRequest{Scope: scope, Path: path, TargetDir: targetDir}, &result)
	return result, err
}

func (c *UnixClient) DirectorySize(ctx context.Context, scope sitefs.Scope, path string) (SizeResult, error) {
	var result SizeResult
	err := c.call(ctx, "/v1/files/size", PathRequest{Scope: scope, Path: path}, &result)
	return result, err
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
		payload.Message = "node agent rejected the file operation"
	}
	return errors.New(payload.Message)
}
