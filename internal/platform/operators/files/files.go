package files

import (
	"context"
	"io"

	"net/http"

	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// ChunkMaxBytes bounds one upload chunk; it is part of the wire contract
// between the control plane, the agent, and browser clients.
const ChunkMaxBytes = 8 * 1024 * 1024

type Entry struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Listing struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

type ReadResult struct {
	Content   string `json:"content"`
	ETag      string `json:"etag"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

type WriteResult struct {
	ETag string `json:"etag"`
}

type Upload struct {
	UploadID string `json:"uploadId"`
}

type CopyResult struct {
	Copied  int   `json:"copied"`
	Bytes   int64 `json:"bytes"`
	Skipped int   `json:"skipped"`
}

type ArchiveResult struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	Entry   Entry `json:"entry"`
}

type ExtractResult struct {
	Written int `json:"written"`
	Skipped int `json:"skipped"`
}

type SizeResult struct {
	Bytes     int64 `json:"bytes"`
	Files     int   `json:"files"`
	Dirs      int   `json:"dirs"`
	Truncated bool  `json:"truncated"`
}

type Operator interface {
	List(ctx context.Context, scope sitefs.Scope, path string) (Listing, error)
	Stat(ctx context.Context, scope sitefs.Scope, path string) (Entry, error)
	Read(ctx context.Context, scope sitefs.Scope, path string) (ReadResult, error)
	Write(ctx context.Context, scope sitefs.Scope, path, content, expectedETag string) (WriteResult, error)
	Mkdir(ctx context.Context, scope sitefs.Scope, path string) (Entry, error)
	Move(ctx context.Context, scope sitefs.Scope, from, to string, overwrite bool) (Entry, error)
	Copy(ctx context.Context, scope sitefs.Scope, from, to string) (CopyResult, error)
	Delete(ctx context.Context, scope sitefs.Scope, path string, recursive bool) error
	UploadBegin(ctx context.Context, scope sitefs.Scope, path string, size int64, overwrite bool) (Upload, error)
	UploadChunk(ctx context.Context, scope sitefs.Scope, uploadID string, offset int64, body io.Reader) error
	UploadCommit(ctx context.Context, scope sitefs.Scope, uploadID string) (Entry, error)
	UploadAbort(ctx context.Context, scope sitefs.Scope, uploadID string) error
	Download(ctx context.Context, scope sitefs.Scope, path string) (io.ReadCloser, Entry, error)
	Archive(ctx context.Context, scope sitefs.Scope, paths []string, target string) (ArchiveResult, error)
	Extract(ctx context.Context, scope sitefs.Scope, path, targetDir string) (ExtractResult, error)
	DirectorySize(ctx context.Context, scope sitefs.Scope, path string) (SizeResult, error)
}

// Error codes cross the agent boundary so handlers on both sides map the
// same failure to the same HTTP status and envelope code.
const (
	CodeInvalid        = "files_invalid"
	CodeNotFound       = "files_not_found"
	CodeExistsConflict = "files_exists_conflict"
	CodeETagConflict   = "files_etag_conflict"
	CodeOffsetConflict = "files_offset_conflict"
)

type OperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Offset  int64  `json:"offset,omitempty"`
}

func (e *OperationError) Error() string { return e.Message }

func StatusFor(code string) int {
	switch code {
	case CodeInvalid:
		return http.StatusUnprocessableEntity
	case CodeNotFound:
		return http.StatusNotFound
	case CodeExistsConflict, CodeETagConflict, CodeOffsetConflict:
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func invalid(message string) *OperationError {
	return &OperationError{Code: CodeInvalid, Message: message}
}

func notFound(message string) *OperationError {
	return &OperationError{Code: CodeNotFound, Message: message}
}

func existsConflict(message string) *OperationError {
	return &OperationError{Code: CodeExistsConflict, Message: message}
}

func etagConflict(message string) *OperationError {
	return &OperationError{Code: CodeETagConflict, Message: message}
}
