package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

func entitlementsSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	NewEntitlementsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestEntitlementsSchemaIsValid(t *testing.T) {
	entitlementsSchema(t)
}

func TestEntitlementsAttributes(t *testing.T) {
	attrs := entitlementsSchema(t).Attributes

	if attr, ok := attrs["scope"]; !ok || !attr.IsRequired() {
		t.Error("scope should be required")
	}
	if attr, ok := attrs["type"]; !ok || !attr.IsOptional() {
		t.Error("type should be optional")
	}
	for _, name := range []string{"id", "names", "entitlements"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

func TestEntitlementsTypeName(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	NewEntitlementsDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_entitlements"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestProviderServesTheEntitlementsDataSource(t *testing.T) {
	for _, factory := range New("test")().(*sonraiProvider).DataSources(context.Background()) {
		resp := &datasource.MetadataResponse{}
		factory().Metadata(context.Background(),
			datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		if resp.TypeName == "sonrai_entitlements" {
			return
		}
	}
	t.Error("provider does not serve sonrai_entitlements")
}
