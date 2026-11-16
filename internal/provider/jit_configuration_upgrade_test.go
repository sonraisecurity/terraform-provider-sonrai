package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestJitAutoPodIsPerEntitlement: on AWS, AutoPoD is isFullAccess on each
// enrolled entitlement. A scope-level auto_pod would be accepted by the
// platform and silently discarded, so it must not exist on the resource.
func TestJitAutoPodIsPerEntitlement(t *testing.T) {
	s := jitSchema(t)
	if _, ok := s.Attributes["auto_pod"]; ok {
		t.Error("scope-level auto_pod should not exist; the platform discards it for AWS")
	}
	nested, ok := s.Attributes["enrolled_entitlements"].(schema.SetNestedAttribute)
	if !ok {
		t.Fatal("enrolled_entitlements should be a set of nested objects")
	}
	attrs := nested.NestedObject.Attributes
	if a, ok := attrs["auto_pod"]; !ok || !a.IsOptional() {
		t.Error("each entitlement should have an optional auto_pod")
	}
	if _, ok := attrs["full_access"]; ok {
		t.Error("full_access was renamed to auto_pod and should be gone")
	}
	if s.Version != 1 {
		t.Errorf("schema version = %d, want 1 so version 0 state is upgraded", s.Version)
	}
}

// TestJitConfigurationUpgradeFromV0 runs version 0 state through the protocol
// server: full_access becomes auto_pod and the scope-level auto_pod is dropped.
func TestJitConfigurationUpgradeFromV0(t *testing.T) {
	ctx := context.Background()
	srv := providerserver.NewProtocol6(New("test")())()
	if _, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{}); err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	v0 := `{"id":"aws/r-abcd/123456789012","scope":"aws/r-abcd/123456789012","deny_first":true,
		"auto_pod":true,"pending":false,"enrolled_entitlements":[
		{"name":"Breakglass","type":"AWS_ACCOUNT_ACCESS_ROLE","full_access":true},
		{"name":"ReadOnly","type":"AWS_PERMISSION_SET","full_access":false}]}`
	resp, err := srv.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "sonrai_jit_configuration",
		Version:  0,
		RawState: &tfprotov6.RawState{JSON: []byte(v0)},
	})
	if err != nil || hasErrors(resp.Diagnostics) {
		t.Fatalf("UpgradeResourceState: err=%v diags=%v", err, resp.Diagnostics)
	}

	objType := jitSchema(t).Type().TerraformType(ctx)
	val, err := resp.UpgradedState.Unmarshal(objType)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var top map[string]tftypes.Value
	if err := val.As(&top); err != nil {
		t.Fatalf("As: %v", err)
	}
	if _, ok := top["auto_pod"]; ok {
		t.Error("upgraded state still has a scope-level auto_pod")
	}
	var deny bool
	_ = top["deny_first"].As(&deny)
	if !deny {
		t.Error("deny_first lost in the upgrade")
	}

	var set []tftypes.Value
	if err := top["enrolled_entitlements"].As(&set); err != nil {
		t.Fatalf("entitlements: %v", err)
	}
	got := map[string]bool{}
	for _, e := range set {
		var fields map[string]tftypes.Value
		_ = e.As(&fields)
		var name string
		var autoPod bool
		_ = fields["name"].As(&name)
		_ = fields["auto_pod"].As(&autoPod)
		got[name] = autoPod
	}
	if len(got) != 2 || !got["Breakglass"] || got["ReadOnly"] {
		t.Errorf("entitlements after upgrade = %v, want Breakglass auto_pod true and ReadOnly false", got)
	}
}
