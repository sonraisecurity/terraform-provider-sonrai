package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func accountDisabledServiceSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewAccountDisabledServiceResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestAccountDisabledServiceSchemaIsValid(t *testing.T) {
	accountDisabledServiceSchema(t)
}

func TestAccountDisabledServiceRequiredAttributes(t *testing.T) {
	attrs := accountDisabledServiceSchema(t).Attributes

	for _, name := range []string{"root_scope", "resource_id", "control_key"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsRequired() {
			t.Errorf("attribute %q should be required", name)
		}
	}

	// Status fields are reported by the platform, never set by configuration.
	for _, name := range []string{"scope", "status", "pending"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

func TestAccountDisabledServiceTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewAccountDisabledServiceResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_account_disabled_service"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestScopePattern(t *testing.T) {
	valid := []string{
		"aws/r-abcd",
		"aws/r-abcd/ou-abcd-1a2b3c/123456789012",
		"gcp/organizations|1234567890/projects|my-project",
		"azure/subscriptions|0000",
	}
	for _, scope := range valid {
		if !scopePattern.MatchString(scope) {
			t.Errorf("scope %q should be accepted", scope)
		}
	}

	invalid := []string{"", "r-abcd", "123456789012", "AWS/r-abcd", "/aws/r-abcd"}
	for _, scope := range invalid {
		if scopePattern.MatchString(scope) {
			t.Errorf("scope %q should be rejected", scope)
		}
	}
}

func TestBuildIDRoundTripsThroughImport(t *testing.T) {
	// Ids carry "|" on GCP and Azure, so the separator has to survive them.
	for _, tc := range []struct{ controlKey, rootScope, resourceID string }{
		{"s3", "aws/r-abcd", "123456789012"},
		{"s3", "aws/r-abcd", "ou-abcd-1a2b3c"},
		{"networkservices", "gcp/organizations|1234567890", "projects|my-project"},
	} {
		id := buildID(accountDisabledServiceModel{
			ControlKey: types.StringValue(tc.controlKey),
			RootScope:  types.StringValue(tc.rootScope),
			ResourceID: types.StringValue(tc.resourceID),
		})
		parts := strings.Split(id, importSeparator)
		if len(parts) != 3 || parts[0] != tc.controlKey || parts[1] != tc.rootScope || parts[2] != tc.resourceID {
			t.Errorf("id %q did not round-trip: got %q", id, parts)
		}
	}
}

// TestAccountDisabledServiceOwnershipPredicate pins the rule Read uses to decide whether
// the block it manages still exists. Getting this wrong means a service that
// someone unblocked or converted to a protect reports "No changes".
func TestAccountDisabledServiceOwnershipPredicate(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		managed bool
	}{
		{"staged or deployed block", client.StatusDisabled, true},
		{"converted to a protect", client.StatusProtected, false},
		{"unmanaged out of band", client.StatusUnprotected, false},
		{"some other control type", "Non-Privileged", false},
		{"status unavailable", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := disabledServiceStillManaged(tc.status); got != tc.managed {
				t.Errorf("status %q treated as managed=%v, want %v", tc.status, got, tc.managed)
			}
		})
	}
}
