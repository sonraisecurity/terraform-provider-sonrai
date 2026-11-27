package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestScopeApproversReadsDirectOwners(t *testing.T) {
	var sent struct {
		Variables map[string]any `json:"variables"`
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"data":{"CloudHierarchyList":{"items":[
			{"scope":"aws/r-abcd/111111111111","approvalType":"ASSIGNED_APPROVERS",
			 "owners":[{"email":"Zed@Example.com"},{"email":"amy@example.com"}],
			 "exceptions":[{"appliesToApprovals":true,"appliedToNotifications":null,
			   "conditions":[{"key":"DayOfWeek","operator":"IN_LIST","value":"SAT,SUN"}],
			   "approvers":[{"name":"Amy","email":"amy@example.com","sonraiUserId":"srn:x::SonraiUser/1"}]}]}]}}}`))
	})
	got, err := c.ScopeApprovers(context.Background(), "aws/r-abcd/111111111111")
	if err != nil {
		t.Fatalf("ScopeApprovers: %v", err)
	}
	want := &ScopeApprovers{
		Scope:        "aws/r-abcd/111111111111",
		Emails:       []string{"amy@example.com", "zed@example.com"},
		ApprovalType: ApprovalAssignedApprovers,
		Exceptions: []ApproverException{{
			AppliesToApprovals: true,
			Conditions:         []ApproverCondition{{Key: "DayOfWeek", Operator: "IN_LIST", Value: "SAT,SUN"}},
			Approvers:          []ExceptionApprover{{Name: "Amy", Email: "amy@example.com", SonraiUserID: "srn:x::SonraiUser/1"}},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	filters, _ := sent.Variables["filters"].(map[string]any)
	scope, _ := filters["scope"].(map[string]any)
	if scope["op"] != "EQ" || scope["value"] != "aws/r-abcd/111111111111" {
		t.Errorf("scope filter = %v, want an EQ match on the scope", scope)
	}
}

func TestScopeApproversDefaultsApprovalType(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"CloudHierarchyList":{"items":[{"scope":"aws/r-abcd","owners":[]}]}}}`))
	})
	got, err := c.ScopeApprovers(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("ScopeApprovers: %v", err)
	}
	if got.ApprovalType != ApprovalExcludeSelf || len(got.Emails) != 0 {
		t.Errorf("got %+v, want exclude-self with no approvers", got)
	}
}

// TestScopeApproversIgnoresOtherScopes: a row for a different scope must not be
// read as this scope's approvers, or the next write would replace the wrong set.
func TestScopeApproversIgnoresOtherScopes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"CloudHierarchyList":{"items":[
			{"scope":"aws/r-abcd/999999999999","owners":[{"email":"x@example.com"}]}]}}}`))
	})
	if _, err := c.ScopeApprovers(context.Background(), "aws/r-abcd/111111111111"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAssignScopeApproversInput(t *testing.T) {
	var sent struct {
		Variables struct {
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"data":{"AssignScopeOwners":{"success":true,"error":""}}}`))
	})
	err := c.AssignScopeApprovers(context.Background(), "aws/r-abcd", []string{"amy@example.com"}, ApprovalEveryoneAtScope, nil)
	if err != nil {
		t.Fatalf("AssignScopeApprovers: %v", err)
	}
	input := sent.Variables.Input
	if input["scope"] != "aws/r-abcd" || input["approvalType"] != ApprovalEveryoneAtScope {
		t.Errorf("input = %v", input)
	}
	// true would delete the approvers of every scope beneath this one.
	if input["overrideChildScopes"] != false {
		t.Errorf("overrideChildScopes = %v, want false", input["overrideChildScopes"])
	}
	want := []any{map[string]any{"email": "amy@example.com"}}
	if !reflect.DeepEqual(input["owners"], want) {
		t.Errorf("owners = %v, want %v", input["owners"], want)
	}
	if calls != 1 {
		t.Errorf("sent %d requests, want 1", calls)
	}
	if _, sentExceptions := input["exceptions"]; sentExceptions {
		t.Errorf("no exceptions to keep, but the input carries %v", input["exceptions"])
	}
}

// TestAssignScopeApproversSendsExceptionsBack: the platform replaces a
// scope's exceptions with the ones in the write, so leaving them out deletes
// rules this provider does not manage.
func TestAssignScopeApproversSendsExceptionsBack(t *testing.T) {
	var sent struct {
		Variables struct {
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"data":{"AssignScopeOwners":{"success":true,"error":""}}}`))
	})
	exceptions := []ApproverException{{
		AppliesToApprovals: true,
		Conditions:         []ApproverCondition{{Key: "DayOfWeek", Operator: "IN_LIST", Value: "SAT,SUN"}},
		Approvers:          []ExceptionApprover{{Email: "amy@example.com", SonraiUserID: "srn:x::SonraiUser/1"}, {SonraiUserID: "srn:x::Group/2"}},
	}}
	if err := c.AssignScopeApprovers(context.Background(), "aws/r-abcd", nil, "", exceptions); err != nil {
		t.Fatalf("AssignScopeApprovers: %v", err)
	}
	want := []any{map[string]any{
		"appliedToNotifications": false,
		"appliesToApprovals":     true,
		"conditions":             []any{map[string]any{"key": "DayOfWeek", "operator": "IN_LIST", "value": "SAT,SUN"}},
		"approvers": []any{
			map[string]any{"email": "amy@example.com", "sonraiUserId": "srn:x::SonraiUser/1"},
			map[string]any{"sonraiUserId": "srn:x::Group/2"},
		},
	}}
	if got := sent.Variables.Input["exceptions"]; !reflect.DeepEqual(got, want) {
		t.Errorf("exceptions = %v\nwant %v", got, want)
	}
}

// TestAssignScopeApproversSendsAnEmptyListToClear: omitting owners would leave
// the existing approvers in place, so clearing has to send the empty list. An
// empty approval type is left out, which the platform reads as "keep it".
func TestAssignScopeApproversSendsAnEmptyListToClear(t *testing.T) {
	var raw map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"data":{"AssignScopeOwners":{"success":true}}}`))
	})
	if err := c.AssignScopeApprovers(context.Background(), "aws/r-abcd", nil, "", nil); err != nil {
		t.Fatalf("AssignScopeApprovers: %v", err)
	}
	input := raw["variables"].(map[string]any)["input"].(map[string]any)
	owners, ok := input["owners"].([]any)
	if !ok || len(owners) != 0 {
		t.Errorf("owners = %#v, want an empty list", input["owners"])
	}
	if _, present := input["approvalType"]; present {
		t.Error("an empty approval type must be omitted, not sent")
	}
}

// TestAssignScopeApproversFailsOnUnsuccessfulPayload: the platform reports some
// refusals as success:false with no GraphQL error. Treating that as done would
// record approvers that were never set.
func TestAssignScopeApproversFailsOnUnsuccessfulPayload(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"AssignScopeOwners":{"success":false,"error":"Each owner must have an email"}}}`))
	})
	err := c.AssignScopeApprovers(context.Background(), "aws/r-abcd", []string{"amy@example.com"}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "Each owner must have an email") {
		t.Errorf("err = %v, want the platform's reason", err)
	}
}
