package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProtectServicePinsSkipSuggestedExceptions(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"ProtectService":{"success":true,
		"issues":{"totalCount":0,"failureCount":0,"items":[]}}}}`, &got)

	_, err := c.ProtectService(context.Background(), "iam", "aws/r-abcd/123456789012",
		[]string{"arn:aws:iam::123456789012:role/Admin"}, nil)
	if err != nil {
		t.Fatalf("ProtectService: %v", err)
	}

	input, _ := got.Variables["input"].(map[string]any)
	// Left false, the platform picks its own exemptions and two applies of the
	// same configuration differ.
	if input["skipSuggestedExceptions"] != true {
		t.Errorf("skipSuggestedExceptions = %v, want true", input["skipSuggestedExceptions"])
	}
	if input["applyAtScope"] != false {
		t.Errorf("applyAtScope = %v, want false", input["applyAtScope"])
	}
	// A nil set must reach the API as [] rather than null.
	sso, ok := input["ssoActorIds"].([]any)
	if !ok || len(sso) != 0 {
		t.Errorf("ssoActorIds = %#v, want an empty list", input["ssoActorIds"])
	}
}

func TestProtectServiceReportsFailureIssues(t *testing.T) {
	c := clientReturning(t, `{"data":{"ProtectService":{"success":false,"issues":{
		"totalCount":1,"failureCount":1,"items":[
		{"scope":"aws/r-abcd/1","message":"account not deployed","scopeFriendlyName":"prod"}]}}}}`, nil)

	_, err := c.ProtectService(context.Background(), "iam", "aws/r-abcd/1", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "account not deployed") || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("error = %v, want the platform's issue message and scope", err)
	}
}

func TestProtectServiceSucceedsWithNonFailingIssues(t *testing.T) {
	// totalCount without failureCount is advisory, not a failure.
	c := clientReturning(t, `{"data":{"ProtectService":{"success":true,"issues":{
		"totalCount":1,"failureCount":0,"items":[{"scope":"aws/r-abcd/1","message":"heads up"}]}}}}`, nil)

	issues, err := c.ProtectService(context.Background(), "iam", "aws/r-abcd/1", nil, nil)
	if err != nil {
		t.Fatalf("ProtectService: %v", err)
	}
	if issues == nil || issues.TotalCount != 1 || len(issues.Messages()) != 1 {
		t.Errorf("issues = %#v, want one advisory issue", issues)
	}
}

// exceptionServer answers the applied and staged exception queries separately.
func exceptionServer(t *testing.T, applied, staged string) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "StagedControlExceptions") {
			_, _ = w.Write([]byte(staged))
			return
		}
		_, _ = w.Write([]byte(applied))
	})
}

func TestControlExceptionsMergesStagedOverApplied(t *testing.T) {
	applied := `{"data":{"AppliedControlExceptions":{"items":[{
		"identities":["arn:b","arn:a"],"ssoActorIds":["someone@example.com"]}]}}}`
	staged := `{"data":{"StagedControlExceptions":{"items":[
		{"identities":["arn:c"],"ssoActorIds":[],"action":"ADD"},
		{"identities":["arn:a"],"ssoActorIds":[],"action":"REMOVE"}]}}}`
	c := exceptionServer(t, applied, staged)

	got, err := c.ControlExceptions(context.Background(), "iam", "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("ControlExceptions: %v", err)
	}
	// Staged intent wins, matching how status reads Protected once staged.
	want := []string{"arn:b", "arn:c"}
	if len(got.Identities) != 2 || got.Identities[0] != want[0] || got.Identities[1] != want[1] {
		t.Errorf("identities = %v, want %v", got.Identities, want)
	}
	if len(got.SSOActorIDs) != 1 || got.SSOActorIDs[0] != "someone@example.com" {
		t.Errorf("ssoActorIds = %v", got.SSOActorIDs)
	}
}

func TestControlExceptionsEmpty(t *testing.T) {
	c := exceptionServer(t,
		`{"data":{"AppliedControlExceptions":{"items":[]}}}`,
		`{"data":{"StagedControlExceptions":{"items":[]}}}`)

	got, err := c.ControlExceptions(context.Background(), "iam", "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("ControlExceptions: %v", err)
	}
	if len(got.Identities) != 0 || len(got.SSOActorIDs) != 0 {
		t.Errorf("got %#v, want empty sets", got)
	}
}

func TestSuggestedExceptionsNotFound(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudServiceUsage":{"items":[]}}}`, nil)

	_, err := c.SuggestedExceptions(context.Background(), "nosuch", "aws/r-abcd/1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestSuggestedExceptionsParsesEntries(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudServiceUsage":{"items":[{"suggestedExceptionList":[
		{"resourceId":"arn:aws:iam::1:role/Admin","ssoActorId":"","name":"Admin","isSSO":false,
		 "lastSensitiveUsed":"iam:CreateUser","lastSensitiveUsedDateString":"2 days ago"},
		{"resourceId":"","ssoActorId":"someone@example.com","name":"Someone","isSSO":true,
		 "lastSensitiveUsed":"iam:CreateRole","lastSensitiveUsedDateString":"yesterday"}]}]}}}`, nil)

	got, err := c.SuggestedExceptions(context.Background(), "iam", "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("SuggestedExceptions: %v", err)
	}
	if len(got) != 2 || got[0].IsSSO || !got[1].IsSSO {
		t.Fatalf("got %#v", got)
	}
	if got[0].LastUsed != "iam:CreateUser" || got[1].SSOActorID != "someone@example.com" {
		t.Errorf("got %#v", got)
	}
}

func TestRemoveControlExceptionStaysStaged(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"RemoveExceptionIdentity":{"response":"STAGED"}}}`, &got)

	err := c.RemoveControlException(context.Background(),
		"aws/r-abcd", "aws/r-abcd/1", "ctl-1", "iam", "arn:aws:iam::1:role/Old", false)
	if err != nil {
		t.Fatalf("RemoveControlException: %v", err)
	}

	input, _ := got.Variables["input"].(map[string]any)
	// Left false, the platform applies the removal through an immediate
	// transaction instead of staging it for the next submission.
	if input["forceStaged"] != true {
		t.Errorf("forceStaged = %v, want true", input["forceStaged"])
	}
	for key, want := range map[string]any{
		"rootScope": "aws/r-abcd", "scope": "aws/r-abcd/1", "controlId": "ctl-1",
		"controlKey": "iam", "identity": "arn:aws:iam::1:role/Old", "isSSO": false,
	} {
		if input[key] != want {
			t.Errorf("input[%s] = %v, want %v", key, input[key], want)
		}
	}
}
