// Package httpx is a small HTTP client wrapper with a user agent, retries and JSON helpers.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Scarsz/serverpack/internal/version"
)

// StatusError is returned when a request completes with a non-2xx status.
type StatusError struct {
	URL    string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	// Show short plain-text/JSON error bodies; HTML pages (e.g. Cloudflare challenges) are just noise.
	if body := strings.TrimSpace(e.Body); body != "" && !strings.HasPrefix(body, "<") {
		body, _, _ = strings.Cut(body, "\n")
		if len(body) > 200 {
			body = body[:200] + "..."
		}
		return fmt.Sprintf("GET %s: HTTP %d: %s", e.URL, e.Status, body)
	}
	return fmt.Sprintf("GET %s: HTTP %d", e.URL, e.Status)
}

// IsNotFound reports whether err is an HTTP 404.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

// Client performs requests with retries on transient failures.
type Client struct {
	HTTP    *http.Client
	Retries int
}

// New returns a Client with sensible defaults.
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 10 * time.Minute}, Retries: 3}
}

// Get performs a GET and returns the response once a 2xx status is received.
// The caller must close the body.
func (c *Client) Get(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", version.UserAgent())
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		lastErr = &StatusError{URL: url, Status: resp.StatusCode, Body: string(body)}
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

// GetJSON GETs url and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	h := map[string]string{"Accept": "application/json"}
	for k, v := range headers {
		h[k] = v
	}
	resp, err := c.Get(ctx, url, h)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

// GetXML GETs url and decodes the XML body into out.
func (c *Client) GetXML(ctx context.Context, url string, headers map[string]string, out any) error {
	resp, err := c.Get(ctx, url, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := xml.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

// PostJSON POSTs body as JSON and decodes the JSON response into out.
func (c *Client) PostJSON(ctx context.Context, url string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{URL: url, Status: resp.StatusCode, Body: string(b)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
