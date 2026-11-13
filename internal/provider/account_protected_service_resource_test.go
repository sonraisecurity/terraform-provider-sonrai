package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

func protectedServiceSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewAccountProtectedServiceResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestAccountProtectedServiceSchemaIsValid(t *testing.T) {
	protectedServiceSchema(t)
}

func TestAccountProtectedServiceAttributes(t *testing.T) {
	attrs := protectedServiceSchema(t).Attributes

	for _, name := range []string{"root_scope", "resource_id", "control_key"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	// Exemptions default to empty, so omitting them means "exempt nobody"
	// rather than leaving the value unknown.
	for _, name := range []string{"exempt_identities", "exempt_sso_actor_ids"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsOptional() || !attr.IsComputed() {
			t.Errorf("%s should be optional and computed", name)
		}
	}
	for _, name := range []string{"scope", "status", "pending"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsComputed() || attr.IsOptional() || attr.IsRequired() {
			t.Errorf("%s should be computed-only", name)
		}
	}
}

func TestAccountProtectedServiceTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewAccountProtectedServiceResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_account_protected_service"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestSuggestedExemptionsSchemaIsValid(t *testing.T) {
	resp := &datasource.SchemaResponse{}
	NewSuggestedExemptionsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}

	var _ dsschema.Schema = resp.Schema
	for _, name := range []string{"identities", "sso_actor_ids", "suggestions", "scope"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok || !attr.IsComputed() {
			t.Errorf("%s should be computed", name)
		}
	}
}

func TestSuggestedExemptionsTypeName(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	NewSuggestedExemptionsDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_suggested_exemptions"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}
