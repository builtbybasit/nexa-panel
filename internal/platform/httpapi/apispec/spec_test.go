package apispec

import (
	"net/http"
	"sort"
	"testing"
)

// Guards that the embedded spec actually parsed at init; a broken embed would
// make every routing assertion elsewhere meaningless. It reads the parsed map
// directly rather than through an accessor, so the package exports nothing for
// tests alone.
func TestSpecParsed(t *testing.T) {
	if operationsErr != nil {
		t.Fatalf("parse operations: %v", operationsErr)
	}
	if len(operations) < 100 {
		t.Fatalf("only %d operations parsed; expected the full contract", len(operations))
	}
}

// Every declared operation has to coexist on one ServeMux, because that is what
// the server builds at boot. ServeMux rejects two patterns that overlap without
// either being more specific — `/databases/users/{id}` against
// `/databases/{id}/backups`, say, which both match `/databases/users/backups`.
// Registering the whole contract here turns that from a panic on startup into a
// failing test, and it catches the pair even when only one method is declared
// today and the conflicting one is added later.
func TestContractRegistersOnOneServeMux(t *testing.T) {
	if operationsErr != nil {
		t.Fatalf("parse operations: %v", operationsErr)
	}

	// Sorted so a failure names the same pair on every run.
	ids := make([]string, 0, len(operations))
	for id := range operations {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	mux := http.NewServeMux()
	for _, id := range ids {
		op := operations[id]
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("operation %s (%s) cannot be routed: %v", id, op.Pattern(), p)
				}
			}()
			mux.Handle(op.Pattern(), http.NotFoundHandler())
		}()
	}
}
