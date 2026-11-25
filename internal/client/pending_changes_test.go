package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPendingChangesDecodesAndScopesTheQuery(t *testing.T) {
	var sent struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":{"PendingChanges":{"items":[
			{"id":"1","changeType":"third_party_access","changeName":"Deny access to Acme","action":"ADD",
			 "scope":"aws/r-abcd","controlKey":"Acme","sonraiUser":"srn:user/1","identities":[]},
			{"id":"2-content_merge","changeType":"third_party_account","changeName":"SONRAI UPDATE","action":"CONTENT_MERGE",
			 "scope":"aws/r-abcd","controlKey":"Acme","sonraiUser":"srn:user/1","identities":["111111111111"]}]}}}`))
	})

	got, err := c.PendingChanges(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2", len(got))
	}
	if got[0].Name != "Deny access to Acme" || got[0].OrganizationWide() {
		t.Errorf("first change = %+v, want a change this identity staged", got[0])
	}
	if !got[1].OrganizationWide() {
		t.Errorf("a third party account update is queued by the platform: %+v", got[1])
	}
	if sent.Variables["rootScope"] != "aws/r-abcd" {
		t.Errorf("rootScope variable = %v", sent.Variables["rootScope"])
	}
	// Changes already inside a running transaction are not this submission's
	// to deploy.
	if !strings.Contains(sent.Query, "inTransaction: {op: EQ, value: false}") {
		t.Error("query must exclude changes already in a transaction")
	}
	if strings.Contains(sent.Query, "aws/r-abcd") {
		t.Error("the root scope must travel as a variable, not in the query text")
	}
}

func TestPendingChangesPages(t *testing.T) {
	full := make([]string, pendingChangesPageSize)
	for i := range full {
		full[i] = `{"id":"x","changeType":"mapping"}`
	}
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"PendingChanges":{"items":[` + strings.Join(full, ",") + `]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"PendingChanges":{"items":[{"id":"last","changeType":"mapping"}]}}}`))
	})
	got, err := c.PendingChanges(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	if len(got) != pendingChangesPageSize+1 || calls != 2 {
		t.Errorf("got %d changes in %d calls, want %d in 2", len(got), calls, pendingChangesPageSize+1)
	}
}

func TestOrganizationWideChangeTypes(t *testing.T) {
	cases := map[string]bool{
		"third_party_account":      true,
		"custom_control_sync":      true,
		"organization_policy_sync": true,
		"third_party_access":       false,
		"mapping":                  false,
		"exemption":                false,
		"organization_policy":      false,
	}
	for changeType, want := range cases {
		if got := (PendingChange{ChangeType: changeType}).OrganizationWide(); got != want {
			t.Errorf("OrganizationWide(%q) = %v, want %v", changeType, got, want)
		}
	}
}
