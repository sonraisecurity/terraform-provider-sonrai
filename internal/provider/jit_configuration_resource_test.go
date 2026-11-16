package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
	for _, name := range []string{"deny_first", "enrolled_entitlements"} {
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

func entitlementObject(t *testing.T, name string, entitlementType attr.Value, autoPod attr.Value) attr.Value {
	t.Helper()
	obj, diags := types.ObjectValue(jitEntitlementAttrTypes, map[string]attr.Value{
		"name": types.StringValue(name), "type": entitlementType, "auto_pod": autoPod,
	})
	if diags.HasError() {
		t.Fatalf("ObjectValue: %s", diags)
	}
	return obj
}

func planEntitlements(t *testing.T, config types.Set) planmodifier.SetResponse {
	t.Helper()
	resp := planmodifier.SetResponse{PlanValue: config}
	entitlementDefaults{}.PlanModifySet(context.Background(),
		planmodifier.SetRequest{ConfigValue: config, PlanValue: config}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("PlanModifySet: %s", resp.Diagnostics)
	}
	return resp
}

// TestEntitlementDefaultsKeepConfiguredAutoPod is the regression for a plan
// that switched AutoPoD off: with type left out and auto_pod = true, nested
// schema defaults planned auto_pod = false.
func TestEntitlementDefaultsKeepConfiguredAutoPod(t *testing.T) {
	objType := types.ObjectType{AttrTypes: jitEntitlementAttrTypes}
	config := types.SetValueMust(objType, []attr.Value{
		entitlementObject(t, "OnCallReadOnly", types.StringNull(), types.BoolValue(true)),
		entitlementObject(t, "OnCallReadWrite", types.StringNull(), types.BoolValue(true)),
		entitlementObject(t, "ReadOnlyAccess", types.StringValue("AWS_SAML_ROLE"), types.BoolNull()),
	})

	got, diags := entitlementsFromSet(context.Background(), planEntitlements(t, config).PlanValue)
	if diags.HasError() {
		t.Fatalf("entitlementsFromSet: %s", diags)
	}
	want := map[string]client.JitEntitlement{
		"OnCallReadOnly":  {Name: "OnCallReadOnly", Type: client.EntitlementPermissionSet, FullAccess: true},
		"OnCallReadWrite": {Name: "OnCallReadWrite", Type: client.EntitlementPermissionSet, FullAccess: true},
		"ReadOnlyAccess":  {Name: "ReadOnlyAccess", Type: "AWS_SAML_ROLE", FullAccess: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entitlements, want %d: %v", len(got), len(want), got)
	}
	for _, e := range got {
		if !reflect.DeepEqual(e, want[e.Name]) {
			t.Errorf("%s = %+v, want %+v", e.Name, e, want[e.Name])
		}
	}
}

func TestEntitlementDefaultsCollapseSpelledOutDefaults(t *testing.T) {
	objType := types.ObjectType{AttrTypes: jitEntitlementAttrTypes}
	config := types.SetValueMust(objType, []attr.Value{
		entitlementObject(t, "A", types.StringNull(), types.BoolNull()),
		entitlementObject(t, "A", types.StringValue(client.EntitlementPermissionSet), types.BoolValue(false)),
	})
	if n := len(planEntitlements(t, config).PlanValue.Elements()); n != 1 {
		t.Errorf("planned %d entitlements, want 1", n)
	}
}

func TestEntitlementDefaultsLeaveUnknownAlone(t *testing.T) {
	objType := types.ObjectType{AttrTypes: jitEntitlementAttrTypes}
	unknown := types.SetUnknown(objType)
	if got := planEntitlements(t, unknown).PlanValue; !got.IsUnknown() {
		t.Errorf("plan = %v, want unknown", got)
	}
	partly := types.SetValueMust(objType, []attr.Value{types.ObjectUnknown(jitEntitlementAttrTypes)})
	if got := planEntitlements(t, partly).PlanValue; !got.Equal(partly) {
		t.Errorf("plan = %v, want it untouched", got)
	}
}

// TestEntitlementNestedAttributesHaveNoDefaults guards against reintroducing
// schema defaults inside the set, which is what caused the AutoPoD bug.
func TestEntitlementNestedAttributesHaveNoDefaults(t *testing.T) {
	set := jitSchema(t).Attributes["enrolled_entitlements"].(schema.SetNestedAttribute)
	if set.NestedObject.Attributes["type"].(schema.StringAttribute).Default != nil {
		t.Error("type must not carry a schema default; entitlementDefaults fills it")
	}
	if set.NestedObject.Attributes["auto_pod"].(schema.BoolAttribute).Default != nil {
		t.Error("auto_pod must not carry a schema default; entitlementDefaults fills it")
	}
}
