// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openEnvClient is a minimal HTTP client for the OpenEnv-compatible contract
// exposed by an RLE environment server (POST /reset, POST /step, GET /state,
// GET /health). It targets a single base URL, which can be a local uvicorn
// server, a local Docker container, or a remote data-plane endpoint.
type openEnvClient struct {
	baseURL    string
	httpClient *http.Client
}

func newOpenEnvClient(baseURL string) *openEnvClient {
	return &openEnvClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// waitForHealth polls GET /health until it returns 200 or the timeout elapses.
func (c *openEnvClient) waitForHealth(ctx context.Context, timeout time.Duration, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ok, err := c.healthOK(ctx); ok {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("environment did not become healthy within %s: %w", timeout, lastErr)
			}
			return fmt.Errorf("environment did not become healthy within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (c *openEnvClient) healthOK(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

// reset calls POST /reset and returns the raw observation as a generic map.
func (c *openEnvClient) reset(ctx context.Context, body map[string]any) (map[string]any, error) {
	return c.call(ctx, http.MethodPost, "/reset", body)
}

// step calls POST /step with the supplied action and returns the observation.
func (c *openEnvClient) step(ctx context.Context, action map[string]any) (map[string]any, error) {
	return c.call(ctx, http.MethodPost, "/step", map[string]any{"action": action})
}

// state calls GET /state.
func (c *openEnvClient) state(ctx context.Context) (map[string]any, error) {
	return c.call(ctx, http.MethodGet, "/state", nil)
}

func (c *openEnvClient) call(ctx context.Context, method string, path string, body any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call environment %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read environment response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("environment returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	result := map[string]any{}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("decode environment response: %w", err)
		}
	}
	return result, nil
}
