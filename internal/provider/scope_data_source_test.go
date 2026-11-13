package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

func scopeSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	NewScopeDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestScopeSchemaIsValid(t *testing.T) {
	scopeSchema(t)
}

func TestScopeAttributes(t *testing.T) {
	attrs := scopeSchema(t).Attributes

	for _, name := range []string{"root_scope", "resource_id"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	if attr, ok := attrs["entry_type"]; !ok || !attr.IsOptional() || !attr.IsComputed() {
		t.Error("entry_type should be optional and computed, so an unset value reports what was matched")
	}
	for _, name := range []string{"id", "scope", "name", "cloud_type", "parent_scope"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

func TestScopeTypeName(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	NewScopeDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_scope"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}
