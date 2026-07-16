package logs

import (
	"context"
	"io"

	"net/http"

	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// Bounds are part of the wire contract between the control plane and the
// agent: no log read ever loads more than one bounded window into memory.
const (
	ReadDefaultMaxBytes = 64 * 1024
	ReadMaxBytes        = 256 * 1024
	ReadDefaultTail     = 200
	ReadMaxTail         = 2000
	ListMaxEntries      = 500
)

type Entry struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Listing struct {
	Items []Entry `json:"items"`
}

// ReadRequest names a log file slash-relative to the site's logs/ directory.
// A nil FromOffset requests tail mode; a non-nil one requests the complete
// lines appended since that byte offset.
type ReadRequest struct {
	Name       string `json:"name"`
	FromOffset *int64 `json:"fromOffset,omitempty"`
	MaxBytes   int    `json:"maxBytes,omitempty"`
	TailLines  int    `json:"tailLines,omitempty"`
	Filter     string `json:"filter,omitempty"`
}

type ReadResult struct {
	Lines      []string `json:"lines"`
	NextOffset int64    `json:"nextOffset"`
	Size       int64    `json:"size"`
	Rotated    bool     `json:"rotated"`
	Truncated  bool     `json:"truncated"`
}

type Operator interface {
	List(ctx context.Context, scope sitefs.Scope) (Listing, error)
	Read(ctx context.Context, scope sitefs.Scope, request ReadRequest) (ReadResult, error)
	Download(ctx context.Context, scope sitefs.Scope, name string) (io.ReadCloser, Entry, error)
}

// Error codes cross the agent boundary so handlers on both sides map the
// same failure to the same HTTP status and envelope code.
const (
	CodeInvalid  = "logs_invalid"
	CodeNotFound = "logs_not_found"
)

type OperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *OperationError) Error() string { return e.Message }

func StatusFor(code string) int {
	switch code {
	case CodeInvalid:
		return http.StatusUnprocessableEntity
	case CodeNotFound:
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func invalid(message string) *OperationError {
	return &OperationError{Code: CodeInvalid, Message: message}
}

func notFound(message string) *OperationError {
	return &OperationError{Code: CodeNotFound, Message: message}
}
