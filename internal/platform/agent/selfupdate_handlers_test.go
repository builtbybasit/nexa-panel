package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
)

type recordingSelfUpdate struct {
	applyCalls int
}

func (*recordingSelfUpdate) Latest(context.Context) (selfupdateoperator.Availability, error) {
	return selfupdateoperator.Availability{}, nil
}

func (operator *recordingSelfUpdate) Apply(context.Context, selfupdateoperator.Change) (selfupdateoperator.Result, error) {
	operator.applyCalls++
	return selfupdateoperator.Result{}, nil
}

func (*recordingSelfUpdate) Rollback(context.Context) (selfupdateoperator.Result, error) {
	return selfupdateoperator.Result{}, nil
}

func TestSelfUpdateRPCRejectsHostPaths(t *testing.T) {
	operator := &recordingSelfUpdate{}
	server := New("unused", "0.1.0", "token", nil, WithSelfUpdateOperator(operator))
	request := httptest.NewRequest(http.MethodPost, "/v1/self-update/apply", strings.NewReader(`{"version":"0.2.0","binaryPath":"/tmp/attacker"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.selfUpdateApplyHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	if operator.applyCalls != 0 {
		t.Fatal("a caller-controlled host path reached the root operator")
	}
}
