package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func jitSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewJitConfigurationResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestJitConfigurationSchemaIsValid(t *testing.T) {
	jitSchema(t)
}

func TestJitConfigurationTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewJitConfigurationResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
	if want := "sonrai_jit_configuration"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

// TestJitConfigurationScopeForcesReplacement: the platform keys a configuration
// on its scope, so moving it is a new configuration, not an update.
func TestJitConfigurationScopeForcesReplacement(t *testing.T) {
	attrs := jitSchema(t).Attributes
	scope, ok := attrs["scope"]
	if !ok || !scope.IsRequired() {
		t.Fatal("scope must be required")
	}
	for _, name := range []string{"deny_first", "auto_pod", "enrolled_entitlements"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !attr.IsOptional() {
			t.Errorf("%s should be optional", name)
		}
	}
	if pending, ok := attrs["pending"]; !ok || !pending.IsComputed() || pending.IsOptional() {
		t.Error("pending should be computed-only")
	}
}

func TestProviderServesTheJitConfigurationResource(t *testing.T) {
	ctx := context.Background()
	for _, factory := range New("test")().(*sonraiProvider).Resources(ctx) {
		resp := &resource.MetadataResponse{}
		factory().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		if resp.TypeName == "sonrai_jit_configuration" {
			return
		}
	}
	t.Error("provider does not serve sonrai_jit_configuration")
}

func TestEntitlementsRoundTrip(t *testing.T) {
	ctx := context.Background()
	want := []client.JitEntitlement{
		{Name: "Breakglass", Type: client.EntitlementAccountAccessRole, FullAccess: true},
		{Name: "PowerUserAccess", Type: client.EntitlementPermissionSet},
	}

	set, diags := entitlementsToSet(ctx, want)
	if diags.HasError() {
		t.Fatalf("entitlementsToSet: %s", diags)
	}
	got, diags := entitlementsFromSet(ctx, set)
	if diags.HasError() {
		t.Fatalf("entitlementsFromSet: %s", diags)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestEntitlementsFromNullSet: a configuration with no enrolments is a real
// state, not an error, and has to reach the mutation as an empty list.
func TestEntitlementsFromNullSet(t *testing.T) {
	got, diags := entitlementsFromSet(context.Background(), types.SetNull(
		types.ObjectType{AttrTypes: jitEntitlementAttrTypes}))
	if diags.HasError() {
		t.Fatalf("entitlementsFromSet: %s", diags)
	}
	if len(got) != 0 {
		t.Errorf("got %d entitlements, want none", len(got))
	}
}

// TestEntitlementsToSetEmpty keeps an empty configuration distinguishable from
// an unknown one.
func TestEntitlementsToSetEmpty(t *testing.T) {
	set, diags := entitlementsToSet(context.Background(), nil)
	if diags.HasError() {
		t.Fatalf("entitlementsToSet: %s", diags)
	}
	if set.IsNull() || set.IsUnknown() {
		t.Error("an empty configuration should be an empty set, not null or unknown")
	}
	if len(set.Elements()) != 0 {
		t.Errorf("got %d elements, want none", len(set.Elements()))
	}
}
