package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// defaultBaseURL represents the API endpoint to call.
const defaultBaseURL = "https://api.abion.com"

const apiKeyHeader = "X-API-KEY"

// Client the Abion API client.
type Client struct {
	apiKey     string
	baseURL    *url.URL
	HTTPClient *http.Client
}

// NewAbionClient Creates a new Client.
func NewAbionClient(apiKey string) *Client {
	baseURL, _ := url.Parse(defaultBaseURL)

	return &Client{
		apiKey:     apiKey,
		baseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// GetZone Returns the full information on a single zone
func (c *Client) GetZone(ctx context.Context, name string) (*APIResponse[*Zone], error) {
	endpoint := c.baseURL.JoinPath("v1", "zones", name)

	req, err := newJSONRequest(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}

	results := &APIResponse[*Zone]{}

	if err := c.do(req, results); err != nil {
		return nil, fmt.Errorf("could not get zone %s: %w", name, err)
	}

	return results, nil
}

// PatchZone Updates a zone by patching it according to JSON Merge Patch format (RFC 7396).
func (c *Client) PatchZone(ctx context.Context, name string, patch ZoneRequest) (*APIResponse[*Zone], error) {
	endpoint := c.baseURL.JoinPath("v1", "zones", name)

	req, err := newJSONRequest(ctx, http.MethodPatch, endpoint, patch)
	if err != nil {
		return nil, err
	}

	results := &APIResponse[*Zone]{}

	if err := c.do(req, results); err != nil {
		return nil, fmt.Errorf("could not update zone %s: %w", name, err)
	}

	return results, nil
}

func (c *Client) do(req *http.Request, result any) error {
	req.Header.Set(apiKeyHeader, c.apiKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return parseError(req, resp)
	}

	if result == nil {
		return nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body %w", err)
	}

	err = json.Unmarshal(raw, result)
	if err != nil {
		return fmt.Errorf("error unmarshalling response %w", err)
	}

	return nil
}

func newJSONRequest(ctx context.Context, method string, endpoint *url.URL, payload any) (*http.Request, error) {
	buf := new(bytes.Buffer)

	if payload != nil {
		err := json.NewEncoder(buf).Encode(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to create request JSON body: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), buf)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

func parseError(req *http.Request, resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)

	zResp := &APIResponse[any]{}
	err := json.Unmarshal(raw, zResp)
	if err != nil {
		// return errutils.NewUnexpectedStatusCodeError(req, resp.StatusCode, raw)
		return fmt.Errorf("error parsing error %w", err)
	}

	return zResp.Error
}
