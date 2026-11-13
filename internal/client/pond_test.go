package client

import (
	"context"
	"reflect"
	"testing"
)

func TestTimeboxedExemptionsKeepsOnlyExpiringGrants(t *testing.T) {
	c := clientReturning(t, `{"data":{"PONDRequests":{"items":[
		{"controlId":"ctl-1","identity":"arn:timeboxed","isSSO":false,"revokeAt":1900000000},
		{"controlId":"ctl-1","identity":"someone@example.com","isSSO":true,"revokeAt":1900000000},
		{"controlId":"ctl-1","identity":"arn:permanent","isSSO":false,"revokeAt":0},
		{"controlId":"CTL-1","identity":"arn:case-folded","isSSO":false,"revokeAt":1900000000},
		{"controlId":"ctl-2","identity":"arn:other-control","isSSO":false,"revokeAt":1900000000},
		{"controlId":"ctl-1","identity":"","isSSO":false,"revokeAt":1900000000}]}}}`, nil)

	got, err := c.TimeboxedExemptions(context.Background(), "aws/r-abcd/1", "ctl-1")
	if err != nil {
		t.Fatalf("TimeboxedExemptions: %v", err)
	}
	// A permanent grant (revokeAt 0) is drift, not a timebox; control ids match
	// case-insensitively; other controls and empty identities are dropped.
	wantIdents := []string{"arn:case-folded", "arn:timeboxed"}
	if !reflect.DeepEqual(got.Identities, wantIdents) {
		t.Errorf("Identities = %v, want %v", got.Identities, wantIdents)
	}
	if !reflect.DeepEqual(got.SSOActorIDs, []string{"someone@example.com"}) {
		t.Errorf("SSOActorIDs = %v", got.SSOActorIDs)
	}
	if !got.Has() {
		t.Error("Has() = false with entries present")
	}
}

func TestTimeboxedExemptionsEmpty(t *testing.T) {
	c := clientReturning(t, `{"data":{"PONDRequests":{"items":[]}}}`, nil)

	got, err := c.TimeboxedExemptions(context.Background(), "aws/r-abcd/1", "ctl-1")
	if err != nil {
		t.Fatalf("TimeboxedExemptions: %v", err)
	}
	if got.Has() {
		t.Errorf("Has() = true for %+v", got)
	}
}
