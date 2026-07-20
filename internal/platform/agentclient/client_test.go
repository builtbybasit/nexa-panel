package agentclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestJSONAuthenticatesAndDecodes(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "http://unix/v1/example" {
			t.Fatalf("URL = %q", request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer rotating-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content type = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"value":"accepted"}`)),
			Request:    request,
		}, nil
	})}
	client := newClient(httpClient, func() (string, error) { return "rotating-token", nil }, "example", "rejected")
	var result struct {
		Value string `json:"value"`
	}
	if err := client.JSON(context.Background(), http.MethodPost, "/v1/example", map[string]string{"input": "value"}, &result); err != nil {
		t.Fatalf("JSON returned an error: %v", err)
	}
	if calls != 1 || result.Value != "accepted" {
		t.Fatalf("calls = %d, result = %+v", calls, result)
	}
}

func TestJSONPreservesTypedAgentFailure(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusConflict,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":"drift","message":"host state changed"}`)),
			Request:    request,
		}, nil
	})}
	client := newClient(httpClient, func() (string, error) { return "token", nil }, "example", "rejected")
	err := client.JSON(context.Background(), http.MethodPost, "/v1/example", nil, nil)
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("error type = %T, want *ResponseError", err)
	}
	if responseErr.StatusCode != http.StatusConflict || responseErr.Code != "drift" || responseErr.Message != "host state changed" {
		t.Fatalf("response error = %+v", responseErr)
	}
}

func TestJSONAcceptsNoContentWithoutDecoding(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}
	client := newClient(httpClient, func() (string, error) { return "token", nil }, "example", "rejected")
	if err := client.JSON(context.Background(), http.MethodDelete, "/v1/example", nil, &struct{}{}); err != nil {
		t.Fatalf("204 response returned an error: %v", err)
	}
}

func TestJSONWrapsTransportFailure(t *testing.T) {
	httpClient := &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unreachable")
		}),
	}
	client := newClient(httpClient, func() (string, error) { return "token", nil }, "example", "rejected")
	if err := client.JSON(context.Background(), http.MethodGet, "/v1/example", nil, nil); err == nil || !strings.Contains(err.Error(), "call example agent") {
		t.Fatalf("transport error = %v", err)
	}
}
