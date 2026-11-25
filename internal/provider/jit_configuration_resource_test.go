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

// TestJitConfigurationScopeIsNamedEitherWay: scope stays usable on its own, as
// 0.1.x configurations have it, and root_scope with resource_id is the
// alternative. None can be required, or one form would shut out the other.
func TestJitConfigurationScopeIsNamedEitherWay(t *testing.T) {
	attrs := jitSchema(t).Attributes
	scope, ok := attrs["scope"]
	if !ok || !scope.IsOptional() || !scope.IsComputed() {
		t.Fatal("scope must be optional and computed")
	}
	for _, name := range []string{"root_scope", "resource_id"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsOptional() || attr.IsComputed() {
			t.Errorf("%s should be optional and not computed", name)
		}
	}
	if got := len(NewJitConfigurationResource().(resource.ResourceWithConfigValidators).ConfigValidators(context.Background())); got != 2 {
		t.Errorf("want 2 config validators (one form only, and the id pair together), got %d", got)
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

// TestReaddressedOnly: naming the same scope the other way must not stage
// anything, and any real change must.
func TestReaddressedOnly(t *testing.T) {
	ctx := context.Background()
	set, diags := entitlementsToSet(ctx, []client.JitEntitlement{{Name: "ReadOnly", Type: client.EntitlementPermissionSet}})
	if diags.HasError() {
		t.Fatal(diags)
	}
	empty, diags := entitlementsToSet(ctx, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	state := jitConfigurationModel{
		Scope:        types.StringValue("aws/r-abcd/ou-ab12-cdef3456/123456789012"),
		DenyFirst:    types.BoolValue(true),
		Entitlements: set,
	}

	same := state
	same.RootScope = types.StringValue("aws/r-abcd")
	same.ResourceID = types.StringValue("123456789012")
	if !readdressedOnly(state, same) {
		t.Error("the same scope named by id should not stage anything")
	}

	for name, change := range map[string]func(*jitConfigurationModel){
		"deny_first":    func(m *jitConfigurationModel) { m.DenyFirst = types.BoolValue(false) },
		"entitlements":  func(m *jitConfigurationModel) { m.Entitlements = empty },
		"scope":         func(m *jitConfigurationModel) { m.Scope = types.StringValue("aws/r-abcd") },
		"unknown scope": func(m *jitConfigurationModel) { m.Scope = types.StringUnknown() },
	} {
		plan := same
		change(&plan)
		if readdressedOnly(state, plan) {
			t.Errorf("a change to %s must be staged", name)
		}
	}
}
