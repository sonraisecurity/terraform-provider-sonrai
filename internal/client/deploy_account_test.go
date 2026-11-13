package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// deployAccountServer answers the two queries ResolveDeployAccount makes.
func deployAccountServer(t *testing.T, hierarchy, metadata string) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "OnboardingMetadata") {
			_, _ = w.Write([]byte(metadata))
			return
		}
		_, _ = w.Write([]byte(hierarchy))
	})
}

const bothRoots = `{"data":{"CloudHierarchyList":{"items":[
	{"resourceId":"111122223333","entryType":"managementAccount"},
	{"resourceId":"r-abcd","entryType":"organization"}]}}}`

func TestResolveDeployAccountPrefersDelegated(t *testing.T) {
	c := deployAccountServer(t, bothRoots, `{"data":{"OnboardingMetadata":{"items":[
		{"resourceId":"r-abcd","organizationId":"o-exampleorgid","onboarded":true,
		 "delegatedAccountId":"999988887777"}]}}}`)

	got, err := c.ResolveDeployAccount(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("ResolveDeployAccount: %v", err)
	}
	// The management account must NOT win when a delegated admin is configured.
	if got.AccountID != "999988887777" || !got.Delegated {
		t.Errorf("got %+v, want the delegated account", got)
	}
	if got.ManagementAccountID != "111122223333" {
		t.Errorf("management account = %q, should still be reported", got.ManagementAccountID)
	}
	if got.OrganizationID != "o-exampleorgid" || !got.Onboarded {
		t.Errorf("got %+v, want organization metadata carried through", got)
	}
}

func TestResolveDeployAccountFallsBackToManagement(t *testing.T) {
	c := deployAccountServer(t, bothRoots, `{"data":{"OnboardingMetadata":{"items":[
		{"resourceId":"r-abcd","organizationId":"o-x","onboarded":true,"delegatedAccountId":""}]}}}`)

	got, err := c.ResolveDeployAccount(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("ResolveDeployAccount: %v", err)
	}
	if got.AccountID != "111122223333" || got.Delegated {
		t.Errorf("got %+v, want the management account with delegated=false", got)
	}
}

func TestResolveDeployAccountWithoutMetadata(t *testing.T) {
	c := deployAccountServer(t, bothRoots, `{"data":{"OnboardingMetadata":{"items":[]}}}`)

	got, err := c.ResolveDeployAccount(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("ResolveDeployAccount: %v", err)
	}
	if got.AccountID != "111122223333" || got.Delegated {
		t.Errorf("got %+v, want the management account", got)
	}
}

func TestResolveDeployAccountNotFound(t *testing.T) {
	c := deployAccountServer(t, `{"data":{"CloudHierarchyList":{"items":[]}}}`, "")

	_, err := c.ResolveDeployAccount(context.Background(), "aws/r-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestResolveDeployAccountRejectsAmbiguousRoots(t *testing.T) {
	c := deployAccountServer(t, `{"data":{"CloudHierarchyList":{"items":[
		{"resourceId":"r-abcd","entryType":"organization"},
		{"resourceId":"r-abcd2","entryType":"organization"}]}}}`,
		`{"data":{"OnboardingMetadata":{"items":[]}}}`)

	_, err := c.ResolveDeployAccount(context.Background(), "aws/r-abcd")
	if err == nil || !strings.Contains(err.Error(), "more than one organization") {
		t.Fatalf("error = %v, want an ambiguity refusal", err)
	}
}
