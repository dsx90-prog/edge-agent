package proxy

import (
	"bytes"
	"context"
	"edge-agent/internal/config"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type APIClient struct {
	client    *http.Client
	config    *config.Config
	baseURL   string
	headers   map[string]string
	authToken string
}

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func NewAPIClient(cfg *config.Config) *APIClient {
	return &APIClient{
		client: &http.Client{
			Timeout: cfg.APIProxy.Timeout,
		},
		config:    cfg,
		baseURL:   cfg.APIProxy.BaseURL,
		headers:   cfg.APIProxy.Headers,
		authToken: cfg.APIProxy.Auth.Token,
	}
}

func (c *APIClient) ExecuteAPICall(ctx context.Context, url string, method string, headers map[string]string, body interface{}) (*APIResponse, error) {
	fullURL := c.baseURL + url
	return c.executeHTTPRequest(ctx, fullURL, method, headers, body)
}

func (c *APIClient) ExecuteHTTPRequest(ctx context.Context, url string, method string, headers map[string]string, body interface{}) (*APIResponse, error) {
	return c.executeHTTPRequest(ctx, url, method, headers, body)
}

func (c *APIClient) executeHTTPRequest(ctx context.Context, url string, method string, headers map[string]string, body interface{}) (*APIResponse, error) {
	// Prepare request body
	var reqBody []byte
	var err error

	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set default headers
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Add custom headers from config
	for key, value := range c.headers {
		if _, exists := headers[key]; !exists {
			req.Header.Set(key, value)
		}
	}

	// Add custom headers from request
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Add authentication header if token is provided and not in request headers
	if c.authToken != "" {
		if _, exists := headers["Authorization"]; !exists {
			authType := c.config.APIProxy.Auth.Type
			if authType == "" {
				authType = "Bearer"
			}
			req.Header.Set("Authorization", fmt.Sprintf("%s %s", authType, c.authToken))
		}
	}

	// Execute request
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check HTTP status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	// Parse response
	var apiResp APIResponse
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		var apiRespI interface{}

		if err := json.Unmarshal(responseBody, &apiRespI); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
		}
		apiResp.Success = resp.StatusCode == 200
		apiResp.Data = apiRespI
		apiResp.Error = string(responseBody)
	}

	log.Printf("API response: %+v", &apiResp)

	return &apiResp, nil
}

func (c *APIClient) HealthCheck(ctx context.Context) error {
	url := c.baseURL + "/health"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed with status: %d", resp.StatusCode)
	}

	return nil
}
