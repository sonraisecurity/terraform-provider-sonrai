package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func regionSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewRegionControlResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestRegionControlSchemaIsValid(t *testing.T) {
	regionSchema(t)
}

func TestRegionControlTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewRegionControlResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
	if want := "sonrai_region_control"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

// TestRegionControlStateIsRequired: enabling is an override rather than the
// absence of a disable, so the state has to be declared and cannot be implied
// by the resource existing.
func TestRegionControlStateIsRequired(t *testing.T) {
	attrs := regionSchema(t).Attributes
	state, ok := attrs["state"]
	if !ok || !state.IsRequired() {
		t.Fatal("state must be required")
	}
	for _, name := range []string{"root_scope", "resource_id", "region_id"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s must be required", name)
		}
	}
	for _, name := range []string{"scope", "previous_state", "status", "pending", "critical", "region_name"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !attr.IsComputed() || attr.IsOptional() {
			t.Errorf("%s should be computed-only", name)
		}
	}
}

// TestRegionStateForTreatsPartialAsTheDeclaredState: a disable at a root with
// one account overriding it leaves the root PARTIALLY_DISABLED. That is still
// the root's control in force, so it must not read as drift and tear the
// resource out of state.
func TestRegionStateForTreatsPartialAsTheDeclaredState(t *testing.T) {
	cases := map[string]string{
		client.RegionDisabled:          regionStateDisabled,
		client.RegionPartiallyDisabled: regionStateDisabled,
		client.RegionEnabled:           regionStateEnabled,
		client.RegionPartiallyEnabled:  regionStateEnabled,
		"NULL_REGION_STATUS":           "",
		"":                             "",
	}
	for status, want := range cases {
		got := regionStateFor(&client.RegionControl{Status: status})
		if got != want {
			t.Errorf("regionStateFor(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestProviderServesTheRegionControlResource(t *testing.T) {
	ctx := context.Background()
	for _, factory := range New("test")().(*sonraiProvider).Resources(ctx) {
		resp := &resource.MetadataResponse{}
		factory().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		if resp.TypeName == "sonrai_region_control" {
			return
		}
	}
	t.Error("provider does not serve sonrai_region_control")
}

// TestRegionDeleteAction pins what destroy does: restore the recorded previous
// state, lift a disable when nothing is recorded, and never stage a disable on
// a guess.
func TestRegionDeleteAction(t *testing.T) {
	cases := []struct {
		name                        string
		declared, previous, current string
		want                        string
	}{
		{"disable restored to enabled", "disabled", "enabled", "disabled", regionDeleteEnable},
		{"override restored to disabled", "enabled", "disabled", "enabled", regionDeleteDisable},
		{"changed outside terraform", "disabled", "enabled", "enabled", regionDeleteDrift},
		{"imported disable is lifted", "disabled", "", "disabled", regionDeleteEnable},
		{"imported override is kept", "enabled", "", "enabled", regionDeleteKeep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := regionDeleteAction(tc.declared, tc.previous, tc.current); got != tc.want {
				t.Errorf("regionDeleteAction(%q, %q, %q) = %q, want %q",
					tc.declared, tc.previous, tc.current, got, tc.want)
			}
		})
	}
}

// TestRegionNoOpReason pins the guard that keeps this resource from adopting
// state it never caused: exact-status matches are rejected, partials pass
// because completing them is a real change.
func TestRegionNoOpReason(t *testing.T) {
	reject := []struct {
		name   string
		before client.RegionControl
		want   string
	}{
		{"already disabled here", client.RegionControl{Status: client.RegionDisabled}, "disabled"},
		{"already enabled by default", client.RegionControl{Status: client.RegionEnabled}, "enabled"},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			if regionNoOpReason(&tc.before, tc.want) == "" {
				t.Error("expected a rejection reason")
			}
		})
	}

	allow := []struct {
		name   string
		before client.RegionControl
		want   string
	}{
		{"disable an enabled region", client.RegionControl{Status: client.RegionEnabled}, "disabled"},
		{"override an ancestor disable", client.RegionControl{Status: client.RegionDisabled}, "enabled"},
		{"complete a partial disable", client.RegionControl{Status: client.RegionPartiallyDisabled}, "disabled"},
		{"complete a partial enable", client.RegionControl{Status: client.RegionPartiallyEnabled}, "enabled"},
	}
	for _, tc := range allow {
		t.Run(tc.name, func(t *testing.T) {
			if reason := regionNoOpReason(&tc.before, tc.want); reason != "" {
				t.Errorf("expected no rejection, got %q", reason)
			}
		})
	}
}
