// Package client is a minimal GraphQL client for the Sonrai platform API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultDomain  = "sonraisecurity.com"
	requestTimeout = 2 * time.Minute
	maxAttempts    = 4
	// Responses are small JSON documents; anything larger is a proxy error page
	// or a misrouted request, and reading it in full would be unbounded.
	maxResponseBytes = 8 << 20
)

// Client executes GraphQL operations against one organization's API.
type Client struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client
	limiter   *rate.Limiter
	// pollInterval is how often the WaitFor helpers re-read a transaction.
	// Submissions take minutes, so polling harder only adds load.
	pollInterval time.Duration
}

// Config describes how to reach an organization's API.
type Config struct {
	Org    string
	Domain string
	Token  string
	// UserAgent identifies the provider build to the server; optional.
	UserAgent string
}

// New validates cfg and returns a Client for it.
func New(cfg Config) (*Client, error) {
	org := strings.TrimSpace(cfg.Org)
	if org == "" {
		return nil, errors.New("org must not be empty")
	}
	if strings.ContainsAny(org, "/?#@:") {
		return nil, fmt.Errorf("org %q must be a bare organization name, not a URL", org)
	}

	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		domain = defaultDomain
	}
	if strings.ContainsAny(domain, "/?#@:") {
		return nil, fmt.Errorf("domain %q must be a bare domain name, not a URL", domain)
	}

	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("token must not be empty")
	}

	return &Client{
		// TLS is not negotiable: the token is a bearer credential.
		endpoint:  fmt.Sprintf("https://%s.%s/graphql", org, domain),
		token:     cfg.Token,
		userAgent: cfg.UserAgent,
		http: &http.Client{
			Timeout: requestTimeout,
		},
		// The API is shared with the console; a Terraform run with hundreds of
		// resources should not be the reason someone's dashboard stops loading.
		limiter:      rate.NewLimiter(rate.Limit(8), 8),
		pollInterval: 10 * time.Second,
	}, nil
}

// Endpoint returns the URL this client talks to.
func (c *Client) Endpoint() string { return c.endpoint }

type request struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type response struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors"`
}

// Execute runs one GraphQL document and decodes the data field into out.
//
// Failed attempts are retried, including on a 5xx, so this is only for
// queries and for mutations that are safe to repeat. A mutation that must not
// run twice goes through ExecuteOnce.
func (c *Client) Execute(ctx context.Context, query string, variables map[string]any, out any) error {
	return c.execute(ctx, query, variables, out, true)
}

// ExecuteOnce runs one GraphQL document without retrying an attempt the server
// may have acted on.
//
// A 5xx or a dropped connection can arrive after the server committed the
// work -- a proxy timing out on a slow SubmitControls still opens the
// transaction -- so for a non-idempotent mutation the only safe response is to
// surface the error. Only rate limiting (429) is retried: it means the request
// was rejected before any work was done.
func (c *Client) ExecuteOnce(ctx context.Context, query string, variables map[string]any, out any) error {
	return c.execute(ctx, query, variables, out, false)
}

func (c *Client) execute(ctx context.Context, query string, variables map[string]any, out any, idempotent bool) error {
	body, err := json.Marshal(request{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		retry, err := c.do(ctx, body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		retryable := retry == retryAlways || (retry == retryIfIdempotent && idempotent)
		if !retryable || attempt == maxAttempts {
			return err
		}

		backoff := time.Duration(1<<(attempt-1)) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return lastErr
}

// retryClass says whether a failed attempt may be repeated.
type retryClass int

const (
	retryNever retryClass = iota
	// retryAlways marks failures where the server cannot have acted: the
	// request was rate-limited away or never fully sent.
	retryAlways
	// retryIfIdempotent marks failures where the server may have acted before
	// the response was lost, such as a 5xx from an intermediary.
	retryIfIdempotent
)

// do performs a single attempt, classifying any failure for the retry loop.
func (c *Client) do(ctx context.Context, body []byte, out any) (retry retryClass, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return retryNever, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// The request may have been sent before the connection failed, so the
		// server could have acted on it.
		return retryIfIdempotent, fmt.Errorf("calling %s: %w", c.endpoint, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return retryIfIdempotent, fmt.Errorf("reading response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return retryNever, fmt.Errorf("response from %s exceeds %d bytes; refusing to decode a truncated document",
			c.endpoint, maxResponseBytes)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// Deliberately does not echo the response body, which can carry request
		// context back into logs.
		return retryNever, fmt.Errorf("%w: HTTP %d from %s", ErrUnauthorized, resp.StatusCode, c.endpoint)
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAlways, fmt.Errorf("HTTP %d from %s", resp.StatusCode, c.endpoint)
	case resp.StatusCode >= 500:
		return retryIfIdempotent, fmt.Errorf("HTTP %d from %s", resp.StatusCode, c.endpoint)
	case resp.StatusCode != http.StatusOK:
		return retryNever, fmt.Errorf("HTTP %d from %s", resp.StatusCode, c.endpoint)
	}

	var decoded response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return retryNever, fmt.Errorf("decoding response from %s: %w", c.endpoint, err)
	}
	if len(decoded.Errors) > 0 {
		return retryNever, Errors(decoded.Errors)
	}
	if out == nil {
		return retryNever, nil
	}
	if err := json.Unmarshal(decoded.Data, out); err != nil {
		return retryNever, fmt.Errorf("decoding data from %s: %w", c.endpoint, err)
	}
	return retryNever, nil
}
