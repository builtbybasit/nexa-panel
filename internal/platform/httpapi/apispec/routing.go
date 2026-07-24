package apispec

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

// HandleOp binds handler to the route and permission the contract declares for
// operationID. It lives here, next to the embedded spec, so packages that cannot
// import webhandler — audit and identity, which webhandler's identity dependency
// would import-cycle — still register routes from the contract without
// re-implementing the permission switch.
//
// An operation carrying an x-permission is registered behind it; a secured
// operation without one is authenticated; an operation with no security
// requirement is registered open.
func HandleOp(registry module.Registry, operationID string, handler http.HandlerFunc) error {
	op, err := Lookup(operationID)
	if err != nil {
		return err
	}
	switch {
	case op.Permission != "":
		return registry.HandleAuthorized(op.Pattern(), op.Permission, handler)
	case op.Secured:
		return registry.HandleAuthenticated(op.Pattern(), handler)
	default:
		return registry.Handle(op.Pattern(), handler)
	}
}

// Register binds every operationID to its handler via HandleOp, stopping at the
// first error. The map keys are the contract's operationIds; method, path, and
// permission come from the spec.
func Register(registry module.Registry, handlers map[string]http.HandlerFunc) error {
	for operationID, handler := range handlers {
		if err := HandleOp(registry, operationID, handler); err != nil {
			return err
		}
	}
	return nil
}
