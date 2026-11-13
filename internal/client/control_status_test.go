package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// capture records the GraphQL document and variables of the last request.
type capture struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func clientReturning(t *testing.T, body string, got *capture) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			_ = json.NewDecoder(r.Body).Decode(got)
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestDisableServiceSendsExpectedOperation(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"DisableService":{"success":true}}}`, &got)

	if err := c.DisableService(context.Background(), "s3", "aws/r-abcd/123456789012"); err != nil {
		t.Fatalf("DisableService: %v", err)
	}
	if !strings.Contains(got.Query, "DisableService") {
		t.Errorf("query did not call DisableService: %s", got.Query)
	}
	if got.Variables["controlKey"] != "s3" || got.Variables["scope"] != "aws/r-abcd/123456789012" {
		t.Errorf("variables = %#v", got.Variables)
	}
	// Pinned to false until there is a reason to expose it; a block that silently
	// extended itself to accounts onboarded later would be a surprising default.
	if got.Variables["applyAtScope"] != false {
		t.Errorf("applyAtScope = %v, want false", got.Variables["applyAtScope"])
	}
}

func TestDisableServiceFailsOnUnsuccessfulResponse(t *testing.T) {
	c := clientReturning(t, `{"data":{"DisableService":{"success":false}}}`, nil)

	// A success:false with no GraphQL error is the API's way of refusing the
	// action; treating it as success would leave Terraform claiming a block
	// that was never staged.
	if err := c.DisableService(context.Background(), "s3", "aws/r-abcd"); err == nil {
		t.Fatal("expected an error when success is false")
	}
}

func TestUnmanageServiceSendsExpectedOperation(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"UnmanageService":{"success":true}}}`, &got)

	if err := c.UnmanageService(context.Background(), "ec2", "aws/r-abcd"); err != nil {
		t.Fatalf("UnmanageService: %v", err)
	}
	if !strings.Contains(got.Query, "UnmanageService") {
		t.Errorf("query did not call UnmanageService: %s", got.Query)
	}
	if _, ok := got.Variables["applyAtScope"]; ok {
		t.Error("UnmanageService must not send applyAtScope")
	}
}

func TestServiceControlStatusParsesStatus(t *testing.T) {
	body := `{"data":{"CloudServiceUsage":{"items":[{"project":"s3","scope":"aws/r-abcd",
		"status":{"status":"Disabled","isPending":true}}]}}}`
	c := clientReturning(t, body, nil)

	status, err := c.ServiceControlStatus(context.Background(), "s3", "aws/r-abcd")
	if err != nil {
		t.Fatalf("ServiceControlStatus: %v", err)
	}
	if status.Status != StatusDisabled || !status.Pending {
		t.Errorf("status = %#v", status)
	}
}

func TestServiceControlStatusNotFoundOnEmptyItems(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudServiceUsage":{"items":[]}}}`, nil)

	_, err := c.ServiceControlStatus(context.Background(), "nosuch", "aws/r-abcd")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestAppliedControlKeysDeduplicatesAndSorts(t *testing.T) {
	// A control can have several mappings at one scope; the caller only needs
	// to know which keys are in force.
	c := clientReturning(t, `{"data":{"AppliedControlMappings":{"items":[
		{"controlKey":"lambda","controlId":"aws:lambda:SensitivePermissions"},
		{"controlKey":"cloud9","controlId":"aws:cloud9:ServiceBlock"},
		{"controlKey":"lambda","controlId":"aws:lambda:SensitivePermissions"}]}}}`, nil)

	keys, err := c.AppliedControlKeys(context.Background(), "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("AppliedControlKeys: %v", err)
	}
	if len(keys) != 2 || keys[0] != "cloud9" || keys[1] != "lambda" {
		t.Errorf("keys = %v, want [cloud9 lambda]", keys)
	}
}

func TestAppliedControlKeysEmptyWhenOnlyStaged(t *testing.T) {
	// A control staged but never deployed has no applied mapping, which is how
	// a destroy knows nothing was ever in force.
	c := clientReturning(t, `{"data":{"AppliedControlMappings":{"items":[]}}}`, nil)

	keys, err := c.AppliedControlKeys(context.Background(), "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("AppliedControlKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("keys = %v, want none", keys)
	}
}
