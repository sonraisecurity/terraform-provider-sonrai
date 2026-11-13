package client

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDisableServicesSendsBulkKeys(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"DisableService":{"success":true,"results":[]}}}`, &got)

	err := c.DisableServices(context.Background(), []string{"cloud9", "sagemaker"}, "aws/r-abcd/123456789012")
	if err != nil {
		t.Fatalf("DisableServices: %v", err)
	}
	if !strings.Contains(got.Query, "controlKeys") || strings.Contains(got.Query, "controlKey:") {
		t.Errorf("query should use controlKeys, not controlKey: %s", got.Query)
	}
	keys, _ := got.Variables["controlKeys"].([]any)
	if len(keys) != 2 {
		t.Errorf("controlKeys = %#v, want two entries", got.Variables["controlKeys"])
	}
}

func TestBulkActionsSkipEmptyInput(t *testing.T) {
	// Nothing to do must not become a call that stages an empty change set.
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be made for an empty key list")
	})
	if err := c.DisableServices(context.Background(), nil, "aws/r-abcd/1"); err != nil {
		t.Errorf("DisableServices: %v", err)
	}
	if err := c.UnmanageServices(context.Background(), []string{}, "aws/r-abcd/1"); err != nil {
		t.Errorf("UnmanageServices: %v", err)
	}
}

func TestBulkActionsReportPerServiceFailures(t *testing.T) {
	c := clientReturning(t, `{"data":{"DisableService":{"success":false,"results":[
		{"controlKey":"cloud9","success":true,"error":""},
		{"controlKey":"nosuch","success":false,"error":"unknown control"}]}}}`, nil)

	err := c.DisableServices(context.Background(), []string{"cloud9", "nosuch"}, "aws/r-abcd/1")
	if err == nil || !strings.Contains(err.Error(), "nosuch") || !strings.Contains(err.Error(), "unknown control") {
		t.Fatalf("error = %v, want the failing service and its reason", err)
	}
}

func TestBlockedServicesFiltersAndSorts(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudServices":{"items":[
		{"controlKey":"sagemaker","status":{"status":"Disabled","isPending":false}},
		{"controlKey":"s3","status":{"status":"Protected","isPending":false}},
		{"controlKey":"cloud9","status":{"status":"Disabled","isPending":true}},
		{"controlKey":"ec2","status":{"status":"Unprotected","isPending":false}}]}}}`, nil)

	blocked, err := c.BlockedServices(context.Background(), "aws/r-abcd/123456789012")
	if err != nil {
		t.Fatalf("BlockedServices: %v", err)
	}
	// Staged blocks count as blocked, matching how status reports staged intent.
	want := []string{"cloud9", "sagemaker"}
	if len(blocked) != len(want) || blocked[0] != want[0] || blocked[1] != want[1] {
		t.Errorf("blocked = %v, want %v", blocked, want)
	}
}

func TestBlockedServicesRejectsContainerScope(t *testing.T) {
	// CloudServices aggregates descendants, so a container reports statuses like
	// "Partially Protected" -- which appears even when the only control beneath
	// is a disable. Treating that scope as an account would silently produce a
	// set that means something else.
	c := clientReturning(t, `{"data":{"CloudServices":{"items":[
		{"controlKey":"cloud9","status":{"status":"Partially Protected","isPending":false}}]}}}`, nil)

	_, err := c.BlockedServices(context.Background(), "aws/r-abcd")
	if err == nil || !strings.Contains(err.Error(), "requires an account scope") {
		t.Fatalf("error = %v, want a container-scope rejection", err)
	}
}
