package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
)

// APIError carries the HTTP status, a stable machine code, and a user-facing
// message. Domain code returns it (usually wrapped through the call stack) so a
// handler no longer hand-selects a status per error branch; Fail maps an
// *APIError to its status and anything else to 500.
//
// It lives in httpapi rather than webhandler because it needs only the transport
// primitives here, not identity — so identity and audit (which webhandler cannot
// import without a cycle) can still decode bodies and map errors through it.
type APIError struct {
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *APIError) Error() string {
	switch {
	case e.Message != "":
		return e.Message
	case e.Err != nil:
		return e.Err.Error()
	default:
		return e.Code
	}
}

func (e *APIError) Unwrap() error { return e.Err }

// NewError builds an APIError with a fixed message.
func NewError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// Errorf builds an APIError whose message is formatted from args. A wrapped
// error (via %w) is preserved for errors.Is/As on the returned value.
func Errorf(status int, code, format string, args ...any) *APIError {
	wrapped := fmt.Errorf(format, args...)
	return &APIError{Status: status, Code: code, Message: wrapped.Error(), Err: errors.Unwrap(wrapped)}
}

// Decode reads a bounded, strict JSON body into a fresh T. On any decode problem
// it returns a 400 APIError carrying the specific reason, so the caller writes
// one line instead of the decode-then-writeError block.
func Decode[T any](w http.ResponseWriter, r *http.Request) (T, *APIError) {
	var value T
	if err := DecodeJSON(w, r, &value); err != nil {
		return value, NewError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	return value, nil
}

// Fail writes err as the standard error envelope. An *APIError is written at its
// own status; sql.ErrNoRows becomes 404; every other error is treated as an
// unexpected server fault and written as 500 without leaking its detail.
func Fail(w http.ResponseWriter, err error) {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		WriteError(w, apiErr.Status, apiErr.Code, apiErr.Error())
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "The requested resource does not exist.")
	default:
		WriteError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}

// FailWith writes err like Fail, but when err is not an *APIError (and not
// sql.ErrNoRows) it uses the supplied status and code and surfaces err.Error().
// This keeps the existing per-branch status/code/message of handlers that have
// not yet moved their domain layer onto APIError, while still centralizing the
// APIError and ErrNoRows cases.
func FailWith(w http.ResponseWriter, err error, status int, code string) {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		WriteError(w, apiErr.Status, apiErr.Code, apiErr.Error())
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "The requested resource does not exist.")
	default:
		WriteError(w, status, code, err.Error())
	}
}

// OK writes value as a JSON body at status. It is a thin, intention-revealing
// alias over WriteJSON.
func OK(w http.ResponseWriter, status int, value any) {
	WriteJSON(w, status, value)
}
