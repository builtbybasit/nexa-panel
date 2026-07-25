// Package apispec is the single source of truth for the control-panel HTTP
// surface. The typed request and response models in models.gen.go are generated
// from the OpenAPI contract by oapi-codegen; the contract itself is embedded as
// JSON so route patterns and their required permission are read from the spec at
// registration time rather than hand-copied into every module.
//
// Regenerate both artifacts with `make openapi-gen` (scripts/openapi-generate.sh)
// after editing anything under openapi/.
package apispec

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed openapi.gen.json
var specJSON []byte

// Operation is the registration-relevant projection of one OpenAPI operation:
// enough to bind a handler to the router and gate it behind the same permission
// the published contract advertises.
type Operation struct {
	OperationID string
	Method      string // upper-case HTTP method, e.g. "GET"
	Path        string // full path including the server prefix, e.g. "/api/v1/mysql-family/engines"
	Permission  string // x-permission extension; "" when the operation declares none
	Secured     bool   // true when the operation carries a non-empty security requirement
}

// Pattern returns the net/http ServeMux routing pattern ("METHOD /path"). Path
// templating uses the same {name} syntax as Go 1.22 ServeMux, so OpenAPI paths
// map across unchanged.
func (o Operation) Pattern() string { return o.Method + " " + o.Path }

var (
	operations    map[string]Operation
	operationsErr error
)

func init() { operations, operationsErr = parseOperations(specJSON) }

var httpMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}, "HEAD": {}, "OPTIONS": {},
}

func parseOperations(raw []byte) (map[string]Operation, error) {
	var doc struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		// The method level stays raw so non-operation keys (a path-level
		// "parameters" array, "summary" string) are skipped instead of failing
		// the whole decode on a type mismatch.
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse embedded openapi spec: %w", err)
	}

	prefix := ""
	if len(doc.Servers) > 0 {
		prefix = strings.TrimRight(doc.Servers[0].URL, "/")
	}

	ops := make(map[string]Operation, 160)
	for path, item := range doc.Paths {
		for methodKey, rawOp := range item {
			method := strings.ToUpper(methodKey)
			if _, ok := httpMethods[method]; !ok {
				continue
			}
			var op struct {
				OperationID string            `json:"operationId"`
				Permission  string            `json:"x-permission"`
				Security    []json.RawMessage `json:"security"`
			}
			if err := json.Unmarshal(rawOp, &op); err != nil {
				return nil, fmt.Errorf("parse operation %s %s: %w", method, path, err)
			}
			if op.OperationID == "" {
				return nil, fmt.Errorf("operation %s %s has no operationId", method, path)
			}
			if existing, dup := ops[op.OperationID]; dup {
				return nil, fmt.Errorf("duplicate operationId %q (%s and %s %s)", op.OperationID, existing.Pattern(), method, path)
			}
			ops[op.OperationID] = Operation{
				OperationID: op.OperationID,
				Method:      method,
				Path:        prefix + path,
				Permission:  op.Permission,
				Secured:     len(op.Security) > 0,
			}
		}
	}
	return ops, nil
}

// Lookup returns the operation metadata for an operationId, or an error when the
// spec failed to parse or the id is undefined. A registration referencing a
// removed or renamed operation therefore fails loudly at startup instead of
// silently serving nothing.
func Lookup(operationID string) (Operation, error) {
	if operationsErr != nil {
		return Operation{}, operationsErr
	}
	op, ok := operations[operationID]
	if !ok {
		return Operation{}, fmt.Errorf("operationId %q is not defined in the openapi spec", operationID)
	}
	return op, nil
}

// Operations returns every parsed operation. The returned map must not be
// mutated. It exists for contract-coverage tests that assert every registered
// operationId is backed by a handler and vice versa.
func Operations() (map[string]Operation, error) {
	if operationsErr != nil {
		return nil, operationsErr
	}
	return operations, nil
}
