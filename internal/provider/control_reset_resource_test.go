package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func resetSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewControlResetResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestControlResetSchemaIsValid(t *testing.T) {
	resetSchema(t)
}

func TestControlResetTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewControlResetResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_control_reset"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestControlResetRequiresConfirmation(t *testing.T) {
	attrs := resetSchema(t).Attributes

	// Without a required confirmation, one variable clears an organization's
	// controls with nothing to catch it.
	if attr, ok := attrs["confirm"]; !ok || !attr.IsRequired() {
		t.Error("confirm must be required")
	}
	if attr, ok := attrs["root_scope"]; !ok || !attr.IsRequired() {
		t.Error("root_scope must be required")
	}

	for _, name := range []string{"transaction_id", "status", "status_message"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

// TestControlResetWaitDefaultsOn pins the default. The stack is deleted before
// the reset runs, so the transaction can reach COMPLETED within the apply --
// which is the whole reason this is a resource rather than fire-and-forget.
func TestControlResetWaitDefaultsOn(t *testing.T) {
	wait, ok := resetSchema(t).Attributes["wait"].(schema.BoolAttribute)
	if !ok {
		t.Fatal("wait is missing or not a bool attribute")
	}
	if wait.Default == nil {
		t.Fatal("wait has no default, so an unset value would mean do not wait")
	}

	resp := &defaults.BoolResponse{}
	wait.Default.DefaultBool(context.Background(), defaults.BoolRequest{}, resp)
	if !resp.PlanValue.ValueBool() {
		t.Error("wait should default to true")
	}
}

// TestControlResetConfirmationCheckedAtPlanTime pins where the check happens.
// By apply time the same run has already deleted the CloudFormation stack this
// resource depends on, so refusing then leaves the firewall torn down with its
// controls still applied.
func TestControlResetConfirmationCheckedAtPlanTime(t *testing.T) {
	cases := map[string]struct {
		rootScope, confirm string
		wantError          bool
	}{
		"matching":    {"aws/r-abcd", "aws/r-abcd", false},
		"mismatched":  {"aws/r-abcd", "aws/r-wxyz", true},
		"placeholder": {"aws/r-real", "aws/r-abcd", true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp := validateResetConfig(t, tc.rootScope, tc.confirm)
			if got := resp.Diagnostics.HasError(); got != tc.wantError {
				t.Fatalf("HasError = %v, want %v: %s", got, tc.wantError, resp.Diagnostics)
			}
			if tc.wantError && !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Confirmation") {
				t.Errorf("unexpected error: %s", resp.Diagnostics)
			}
		})
	}
}

// TestControlResetUnknownConfirmationPasses covers a confirm that is not known
// until apply. Validation cannot compare it, and failing closed there would
// reject configurations that are correct.
func TestControlResetUnknownConfirmationPasses(t *testing.T) {
	resp := &resource.ValidateConfigResponse{}
	NewControlResetResource().(*controlResetResource).ValidateConfig(context.Background(),
		resource.ValidateConfigRequest{Config: resetConfig(t, tftypes.UnknownValue, "aws/r-abcd")}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("an unknown root_scope should not fail validation: %s", resp.Diagnostics)
	}
}

func validateResetConfig(t *testing.T, rootScope, confirm string) *resource.ValidateConfigResponse {
	t.Helper()
	resp := &resource.ValidateConfigResponse{}
	NewControlResetResource().(*controlResetResource).ValidateConfig(context.Background(),
		resource.ValidateConfigRequest{Config: resetConfig(t, rootScope, confirm)}, resp)
	return resp
}

// resetConfig builds a tfsdk.Config holding root_scope and confirm, with every
// other attribute null. rootScope may be tftypes.UnknownValue.
func resetConfig(t *testing.T, rootScope any, confirm string) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	s := resetSchema(t)

	return tfsdk.Config{
		Schema: s,
		Raw: tftypes.NewValue(s.Type().TerraformType(ctx), map[string]tftypes.Value{
			"id":             tftypes.NewValue(tftypes.String, nil),
			"root_scope":     tftypes.NewValue(tftypes.String, rootScope),
			"confirm":        tftypes.NewValue(tftypes.String, confirm),
			"wait":           tftypes.NewValue(tftypes.Bool, nil),
			"wait_timeout":   tftypes.NewValue(tftypes.String, nil),
			"transaction_id": tftypes.NewValue(tftypes.String, nil),
			"status":         tftypes.NewValue(tftypes.String, nil),
			"status_message": tftypes.NewValue(tftypes.String, nil),
		}),
	}
}
