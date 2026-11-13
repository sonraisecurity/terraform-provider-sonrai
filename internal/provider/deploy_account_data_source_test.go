package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

func deployAccountSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	NewDeployAccountDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestDeployAccountSchemaIsValid(t *testing.T) {
	deployAccountSchema(t)
}

func TestDeployAccountAttributes(t *testing.T) {
	attrs := deployAccountSchema(t).Attributes

	if attr, ok := attrs["root_scope"]; !ok || !attr.IsRequired() {
		t.Error("root_scope should be required")
	}
	for _, name := range []string{"id", "account_id", "delegated", "management_account_id", "organization_id", "onboarded"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

func TestDeployAccountTypeName(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	NewDeployAccountDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_deploy_account"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}
