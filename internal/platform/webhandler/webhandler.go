// Package webhandler holds the request-handling conventions shared by every
// control-plane feature module: spec-driven route registration, actor
// resolution, bounded JSON decoding, and one error-to-status mapping. It sits
// one layer above httpapi (which owns the raw transport primitives) because it
// also depends on identity and the spec contract, which httpapi must not.
//
// A module handler shrinks to its business call plus these helpers instead of
// re-deriving the auth guard, the decode-and-400 block, the response envelope,
// and the route/permission table in every file.
package webhandler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi/apispec"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

// APIError carries the HTTP status, a stable machine code, and a user-facing
// message. Domain code returns it (usually wrapped through the call stack) so a
// handler no longer hand-selects a status per error branch; Fail maps an
// *APIError to its status and anything else to 500.
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

var errUnauthenticated = NewError(http.StatusUnauthorized, "authentication_required", "Sign in to continue.")

// Actor resolves the authenticated user placed on the request context by the
// control-plane authentication middleware. It returns the shared 401 APIError
// when no user is present, replacing the per-module actorID guard.
func Actor(r *http.Request) (identity.User, *APIError) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return identity.User{}, errUnauthenticated
	}
	return user, nil
}

// ActorID is the pointer form many domain methods accept for their audit actor
// argument. The bool reports whether a user was present.
func ActorID(r *http.Request) (*string, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return nil, false
	}
	return &user.ID, true
}

// Decode reads a bounded, strict JSON body into a fresh T. On any decode problem
// it returns a 400 APIError carrying the specific reason, so the caller writes
// one line instead of the decode-then-writeError block.
func Decode[T any](w http.ResponseWriter, r *http.Request) (T, *APIError) {
	var value T
	if err := httpapi.DecodeJSON(w, r, &value); err != nil {
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
		httpapi.WriteError(w, apiErr.Status, apiErr.Code, apiErr.Error())
	case errors.Is(err, sql.ErrNoRows):
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "The requested resource does not exist.")
	default:
		httpapi.WriteError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
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
		httpapi.WriteError(w, apiErr.Status, apiErr.Code, apiErr.Error())
	case errors.Is(err, sql.ErrNoRows):
		httpapi.WriteError(w, http.StatusNotFound, "not_found", "The requested resource does not exist.")
	default:
		httpapi.WriteError(w, status, code, err.Error())
	}
}

// OK writes value as a JSON body at status. It is a thin, intention-revealing
// alias over httpapi.WriteJSON so a handler file needs only this package.
func OK(w http.ResponseWriter, status int, value any) {
	httpapi.WriteJSON(w, status, value)
}

// HandleOp binds handler to the route and permission the OpenAPI contract
// declares for operationID, so the per-module route tables collapse to a list of
// (operationID, handler) pairs and a renamed or deleted operation fails
// registration at startup instead of drifting silently. The routing itself lives
// in apispec (cycle-free); this is the convenience re-export for the modules that
// already depend on webhandler for Actor/Decode/Fail.
func HandleOp(registry module.Registry, operationID string, handler http.HandlerFunc) error {
	return apispec.HandleOp(registry, operationID, handler)
}

// Register binds every operationID to its handler via HandleOp, replacing the
// per-module route table and its loop.
func Register(registry module.Registry, handlers map[string]http.HandlerFunc) error {
	return apispec.Register(registry, handlers)
}
