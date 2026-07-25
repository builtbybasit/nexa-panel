// Package webhandler holds the identity-aware request helpers shared by feature
// modules: actor resolution and spec-driven route registration. The
// identity-independent conventions — Decode, Fail, and the JSON envelope — live
// in httpapi so identity and audit, which webhandler cannot import without a
// cycle, can use them too. This package re-exports them so a module that already
// depends on webhandler for Actor needs only this import.
package webhandler

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi/apispec"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

// Decode forwards to httpapi so a handler file that already imports webhandler
// (for Actor) reaches it here, alongside Fail.
func Decode[T any](w http.ResponseWriter, r *http.Request) (T, *httpapi.APIError) {
	return httpapi.Decode[T](w, r)
}

// Fail forwards to httpapi.Fail.
func Fail(w http.ResponseWriter, err error) { httpapi.Fail(w, err) }

var errUnauthenticated = httpapi.NewError(http.StatusUnauthorized, "authentication_required", "Sign in to continue.")

// Actor resolves the authenticated user placed on the request context by the
// control-panel authentication middleware. It returns the shared 401 APIError
// when no user is present, replacing the per-module actorID guard.
func Actor(r *http.Request) (identity.User, *httpapi.APIError) {
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

// Register binds every operationID to the route and permission the OpenAPI
// contract declares for it, replacing the per-module route table and its loop.
// The routing itself lives in apispec (cycle-free); this is the re-export for
// modules that already depend on webhandler.
func Register(registry module.Registry, handlers map[string]http.HandlerFunc) error {
	return apispec.Register(registry, handlers)
}
