package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := New(Config{Org: "testorg", Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.endpoint = server.URL
	// Real polling cadence would stall the wait tests.
	c.pollInterval = 10 * time.Millisecond
	return c
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := map[string]Config{
		"empty org":     {Token: "t"},
		"org as url":    {Org: "https://x.sonraisecurity.com", Token: "t"},
		"empty token":   {Org: "testorg"},
		"domain as url": {Org: "testorg", Domain: "https://sonraisecurity.com", Token: "t"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestNewBuildsHTTPSEndpoint(t *testing.T) {
	c, err := New(Config{Org: "testorg", Token: "t"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "https://testorg.sonraisecurity.com/graphql"; c.Endpoint() != want {
		t.Errorf("endpoint = %q, want %q", c.Endpoint(), want)
	}
}

func TestExecuteSendsBearerToken(t *testing.T) {
	var gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	if err := c.Execute(context.Background(), "query { x }", nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "Bearer test-token"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestExecuteSurfacesGraphQLErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"scope not permitted"}]}`))
	})

	err := c.Execute(context.Background(), "query { x }", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "scope not permitted") {
		t.Fatalf("expected the GraphQL error message, got %v", err)
	}
}

func TestExecuteDoesNotRetryUnauthorized(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"token expired for user@example.com"}`))
	})

	err := c.Execute(context.Background(), "query { x }", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 1 {
		t.Errorf("made %d calls, want 1: a rejected token will not become valid on retry", calls.Load())
	}
	// The body can carry identifying detail; it must not reach diagnostics or logs.
	if strings.Contains(err.Error(), "user@example.com") {
		t.Errorf("error leaked the response body: %v", err)
	}
}

func TestExecuteRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	})

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Execute(context.Background(), "query { ok }", nil, &out); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK || calls.Load() != 3 {
		t.Errorf("out.OK = %v after %d calls, want true after 3", out.OK, calls.Load())
	}
}

func TestExecuteOnceDoesNotRetryServerErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})

	err := c.ExecuteOnce(context.Background(), "mutation { x }", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 1 {
		t.Errorf("made %d calls, want 1: the server may have committed before the 502", calls.Load())
	}
}

func TestExecuteOnceRetriesRateLimiting(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	if err := c.ExecuteOnce(context.Background(), "mutation { x }", nil, nil); err != nil {
		t.Fatalf("ExecuteOnce: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("made %d calls, want 2: a 429 was rejected before any work was done", calls.Load())
	}
}

func TestExecuteSendsUserAgent(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(server.Close)

	c, err := New(Config{Org: "testorg", Token: "t", UserAgent: "terraform-provider-sonrai/1.2.3"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.endpoint = server.URL

	if err := c.Execute(context.Background(), "query { x }", nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "terraform-provider-sonrai/1.2.3"; gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

func TestExecuteSendsVariables(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Variables
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	vars := map[string]any{"controlKey": "s3", "applyAtScope": true}
	if err := c.Execute(context.Background(), "mutation { x }", vars, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got["controlKey"] != "s3" || got["applyAtScope"] != true {
		t.Errorf("variables = %#v, want controlKey=s3 applyAtScope=true", got)
	}
}
