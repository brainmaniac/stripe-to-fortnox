package fortnox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

const apiBase = "https://api.fortnox.se/3/"

// APIClient is an HTTP client for the Fortnox REST API with rate limiting and token refresh.
type APIClient struct {
	oauth   *OAuthClient
	limiter *rate.Limiter
	http    *http.Client
}

func NewAPIClient(oauth *OAuthClient) *APIClient {
	return &APIClient{
		oauth:   oauth,
		limiter: rate.NewLimiter(rate.Every(time.Second/4), 4),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *APIClient) do(ctx context.Context, method, path string, body interface{}) ([]byte, int, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, 0, err
	}

	token, err := c.oauth.GetValidAccessToken(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("get fortnox access token: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	return respBody, resp.StatusCode, nil
}

// Post sends a POST request to the Fortnox API, retrying on rate-limit errors.
func (c *APIClient) Post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*5) * time.Second)
		}
		respBody, statusCode, err := c.do(ctx, http.MethodPost, path, body)
		if err != nil {
			return nil, err
		}
		if statusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("fortnox rate limited (429)")
			continue
		}
		if statusCode >= 400 {
			return nil, fmt.Errorf("fortnox api error %d: %s", statusCode, string(respBody))
		}
		return respBody, nil
	}
	return nil, lastErr
}

// Put sends a PUT request to the Fortnox API.
func (c *APIClient) Put(ctx context.Context, path string, body interface{}) ([]byte, error) {
	respBody, statusCode, err := c.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return nil, err
	}
	if statusCode >= 400 {
		return nil, fmt.Errorf("fortnox api error %d: %s", statusCode, string(respBody))
	}
	return respBody, nil
}

// Get sends a GET request to the Fortnox API.
func (c *APIClient) Get(ctx context.Context, path string) ([]byte, error) {
	respBody, statusCode, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if statusCode >= 400 {
		return nil, fmt.Errorf("fortnox api error %d: %s", statusCode, string(respBody))
	}
	return respBody, nil
}
