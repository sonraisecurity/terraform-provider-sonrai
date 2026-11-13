package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func authoritativeSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewDisabledServicesAuthoritativeResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestDisabledServicesAuthoritativeSchemaIsValid(t *testing.T) {
	authoritativeSchema(t)
}

func TestDisabledServicesAuthoritativeAttributes(t *testing.T) {
	attrs := authoritativeSchema(t).Attributes

	for _, name := range []string{"root_scope", "resource_id", "control_keys"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	for _, name := range []string{"id", "scope"} {
		if attr, ok := attrs[name]; !ok || !attr.IsComputed() || attr.IsOptional() {
			t.Errorf("%s should be computed-only", name)
		}
	}
}

func TestDisabledServicesAuthoritativeTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewDisabledServicesAuthoritativeResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_account_disabled_services_authoritative"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestDifference(t *testing.T) {
	// The reconcile diff decides what gets staged. Anything blocked on the
	// server but not declared must appear in the remove set -- that is the
	// authoritative promise.
	current := []string{"bedrock", "cloud9", "guardduty"}
	desired := []string{"cloud9", "sagemaker"}

	add := difference(desired, current)
	remove := difference(current, desired)

	if len(add) != 1 || add[0] != "sagemaker" {
		t.Errorf("add = %v, want [sagemaker]", add)
	}
	if len(remove) != 2 || remove[0] != "bedrock" || remove[1] != "guardduty" {
		t.Errorf("remove = %v, want [bedrock guardduty]", remove)
	}
}

func TestDifferenceEmptyCases(t *testing.T) {
	if got := difference(nil, []string{"a"}); len(got) != 0 {
		t.Errorf("difference(nil, …) = %v, want empty", got)
	}
	// Declaring nothing removes everything currently blocked.
	if got := difference([]string{"a", "b"}, nil); len(got) != 2 {
		t.Errorf("difference(…, nil) = %v, want both entries", got)
	}
}

func TestSetToStringsSorts(t *testing.T) {
	ctx := context.Background()
	set, diags := stringsToSet(ctx, []string{"sagemaker", "cloud9"})
	if diags.HasError() {
		t.Fatalf("stringsToSet: %s", diags)
	}
	got, diags := setToStrings(ctx, set)
	if diags.HasError() {
		t.Fatalf("setToStrings: %s", diags)
	}
	if len(got) != 2 || got[0] != "cloud9" || got[1] != "sagemaker" {
		t.Errorf("got %v, want sorted [cloud9 sagemaker]", got)
	}
}

func TestAccountBlocksID(t *testing.T) {
	id := authoritativeID(disabledServicesAuthoritativeModel{
		RootScope:  types.StringValue("aws/r-abcd"),
		ResourceID: types.StringValue("123456789012"),
	})
	if want := "aws/r-abcd@123456789012"; id != want {
		t.Errorf("id = %q, want %q", id, want)
	}
}
