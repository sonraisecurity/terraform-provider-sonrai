package client

import (
	"context"
	"strings"
	"testing"
)

func TestResetCloudRestrictionsReturnsTransactionID(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"ResetCloudRestrictions":{"success":true,"transactionId":"tx-reset"}}}`, &got)

	id, err := c.ResetCloudRestrictions(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("ResetCloudRestrictions: %v", err)
	}
	if id != "tx-reset" {
		t.Errorf("id = %q, want tx-reset", id)
	}
	if got.Variables["rootScope"] != "aws/r-abcd" {
		t.Errorf("variables = %#v", got.Variables)
	}
}

func TestResetCloudRestrictionsFailsWithoutTransactionID(t *testing.T) {
	// Reporting success with no transaction gives the caller nothing to track,
	// so treat it as a failure rather than a silent no-op.
	c := clientReturning(t, `{"data":{"ResetCloudRestrictions":{"success":true,"transactionId":""}}}`, nil)

	_, err := c.ResetCloudRestrictions(context.Background(), "aws/r-abcd")
	if err == nil || !strings.Contains(err.Error(), "no transaction id") {
		t.Fatalf("error = %v, want a missing-transaction error", err)
	}
}

func TestResetCloudRestrictionsFailsOnUnsuccessful(t *testing.T) {
	c := clientReturning(t, `{"data":{"ResetCloudRestrictions":{"success":false,"transactionId":""}}}`, nil)

	if _, err := c.ResetCloudRestrictions(context.Background(), "aws/r-abcd"); err == nil {
		t.Fatal("expected an error when success is false")
	}
}
