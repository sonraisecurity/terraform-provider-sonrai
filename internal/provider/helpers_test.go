package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestStringsToSetNilIsEmptyNotNull(t *testing.T) {
	set, diags := stringsToSet(context.Background(), nil)
	if diags.HasError() {
		t.Fatalf("diags: %s", diags)
	}
	if set.IsNull() {
		t.Fatal("nil slice became a null set; it must be the empty set or an imported empty account never converges")
	}
	if n := len(set.Elements()); n != 0 {
		t.Fatalf("got %d elements, want 0", n)
	}
}

func TestDifference(t *testing.T) {
	// Used to work out which exemptions to remove from a protect: anything in
	// the first set and not the second must come back, sorted.
	current := []string{"bedrock", "cloud9", "guardduty"}
	desired := []string{"cloud9", "sagemaker"}

	add := difference(desired, current)
	remove := difference(current, desired)

	if len(add) != 1 || add[0] != "sagemaker" {
		t.Errorf("add = %v, want [sagemaker]", add)
	}
	if len(remove) != 2 || remove[0] != "bedrock" || remove[1] != "guardduty" {
		t.Errorf("remove = %v, want [bedrock guardduty]", remove)
	}
}

func TestDifferenceEmptyCases(t *testing.T) {
	if got := difference(nil, []string{"a"}); len(got) != 0 {
		t.Errorf("difference(nil, …) = %v, want empty", got)
	}
	if got := difference([]string{"a", "b"}, nil); len(got) != 2 {
		t.Errorf("difference(…, nil) = %v, want both entries", got)
	}
}

func TestSetToStringsSorts(t *testing.T) {
	ctx := context.Background()
	set, diags := stringsToSet(ctx, []string{"sagemaker", "cloud9"})
	if diags.HasError() {
		t.Fatalf("stringsToSet: %s", diags)
	}
	got, diags := setToStrings(ctx, set)
	if diags.HasError() {
		t.Fatalf("setToStrings: %s", diags)
	}
	if len(got) != 2 || got[0] != "cloud9" || got[1] != "sagemaker" {
		t.Errorf("got %v, want sorted [cloud9 sagemaker]", got)
	}
}

// TestStagingResourcesExposeRevision: the revision is what a submission's
// triggers reference, and it only works because it has no plan modifier. With
// UseStateForUnknown it would stay known through a plan that reverts drift, and
// the revert would be staged without ever being submitted.
func TestStagingResourcesExposeRevision(t *testing.T) {
	ctx := context.Background()
	factories := map[string]func() resource.Resource{
		"account_disabled_service":  NewAccountDisabledServiceResource,
		"account_protected_service": NewAccountProtectedServiceResource,
		"exempted_identity":         NewExemptedIdentityResource,
		"jit_configuration":         NewJitConfigurationResource,
		"region_control":            NewRegionControlResource,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			resp := &resource.SchemaResponse{}
			factory().Schema(ctx, resource.SchemaRequest{}, resp)
			attr, ok := resp.Schema.Attributes["revision"].(schema.StringAttribute)
			if !ok {
				t.Fatal("missing revision string attribute")
			}
			if !attr.Computed || attr.Optional || attr.Required {
				t.Error("revision must be computed only")
			}
			if len(attr.PlanModifiers) != 0 {
				t.Error("revision must have no plan modifiers, so that it is unknown in any plan that changes the resource")
			}
		})
	}
}

func TestNewRevisionIsUnique(t *testing.T) {
	a, b := newRevision(), newRevision()
	if a.ValueString() == "" || a.Equal(b) {
		t.Errorf("revisions %q and %q should be non-empty and distinct", a.ValueString(), b.ValueString())
	}
}

// TestEnsureRevisionKeepsAKnownValue: a refresh that moved the revision would
// change the submission's triggers on every plan.
func TestEnsureRevisionKeepsAKnownValue(t *testing.T) {
	known := types.StringValue("abc")
	ensureRevision(&known)
	if known.ValueString() != "abc" {
		t.Errorf("known revision changed to %q", known.ValueString())
	}
	for name, value := range map[string]types.String{
		"null": types.StringNull(), "unknown": types.StringUnknown(), "empty": types.StringValue(""),
	} {
		ensureRevision(&value)
		if value.IsNull() || value.IsUnknown() || value.ValueString() == "" {
			t.Errorf("%s revision was not filled", name)
		}
	}
}
