package php

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// UnixClient is the control-plane-side Operator implementation. It talks to the
// privileged agent over its Unix socket, authenticating with the shared token.
type UnixClient struct {
	client *agentclient.Client
}

// NewUnixClient dials the agent socket. The long timeout accommodates apt runs
// when an extension is installed.
func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "php", "node agent rejected PHP operation", 30*time.Minute)}
}

func (c *UnixClient) Versions(ctx context.Context) ([]string, error) {
	var result struct {
		Versions []string `json:"versions"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/php/versions", nil, &result)
	return result.Versions, err
}

func (c *UnixClient) Extensions(ctx context.Context, version string) ([]Extension, error) {
	var result struct {
		Extensions []Extension `json:"extensions"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/php/extensions?version="+url.QueryEscape(version), nil, &result)
	return result.Extensions, err
}

func (c *UnixClient) Settings(ctx context.Context, version string) ([]Directive, error) {
	var result struct {
		Directives []Directive `json:"directives"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/php/settings?version="+url.QueryEscape(version), nil, &result)
	return result.Directives, err
}

func (c *UnixClient) SiteSettings(ctx context.Context, scope SiteScope) ([]Directive, error) {
	var result struct {
		Directives []Directive `json:"directives"`
	}
	err := c.call(ctx, http.MethodPost, "/v1/php/sites/settings", scope, &result)
	return result.Directives, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var result Plan
	err := c.call(ctx, http.MethodPost, "/v1/php/plan", change, &result)
	return result, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Observation, error) {
	var result Observation
	err := c.call(ctx, http.MethodPost, "/v1/php/apply", plan, &result)
	return result, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
