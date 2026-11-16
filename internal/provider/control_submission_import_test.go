package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// planTriggersAfterImport imports a submission through the protocol server,
// then plans triggers onto it as the first apply after an import would. It
// reports whether that plan replaces the submission. withPrivate false drops
// the private state the import returned, standing in for a submission that
// was created rather than imported.
func planTriggersAfterImport(t *testing.T, withPrivate bool, priorTriggers map[string]string, checkPlanned func(map[string]tftypes.Value)) bool {
	t.Helper()
	ctx := context.Background()
	srv := providerserver.NewProtocol6(New("test")())()

	if _, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{}); err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	imp, err := srv.ImportResourceState(ctx, &tfprotov6.ImportResourceStateRequest{
		TypeName: "sonrai_control_submission",
		ID:       "11111111-2222-3333-4444-555555555555",
	})
	if err != nil || hasErrors(imp.Diagnostics) || len(imp.ImportedResources) != 1 {
		t.Fatalf("ImportResourceState: err=%v diags=%v", err, imp.Diagnostics)
	}
	private := imp.ImportedResources[0].Private
	if !withPrivate {
		private = nil
	}

	objType := submissionSchema(t).Type().TerraformType(ctx).(tftypes.Object)
	mapType := tftypes.Map{ElementType: tftypes.String}
	triggers := func(m map[string]string) tftypes.Value {
		if m == nil {
			return tftypes.NewValue(mapType, nil)
		}
		vals := map[string]tftypes.Value{}
		for k, v := range m {
			vals[k] = tftypes.NewValue(tftypes.String, v)
		}
		return tftypes.NewValue(mapType, vals)
	}
	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
	null := func(t tftypes.Type) tftypes.Value { return tftypes.NewValue(t, nil) }

	// What the post-import Read leaves in state.
	read := map[string]tftypes.Value{
		"id":                str("11111111-2222-3333-4444-555555555555"),
		"transaction_id":    str("11111111-2222-3333-4444-555555555555"),
		"root_scope":        str("aws/r-abcd"),
		"triggers":          triggers(priorTriggers),
		"wait":              tftypes.NewValue(tftypes.Bool, true),
		"wait_timeout":      str(defaultWaitTimeout),
		"status":            str("COMPLETED"),
		"status_message":    str("completed"),
		"template_location": str("https://bucket.s3.amazonaws.com/template.json"),
		// Known at plan because the imported submission is read, not created:
		// this is what keeps the stack's template_url from going null.
		"stack_template_location": str("https://bucket.s3.amazonaws.com/template.json"),
	}
	configured := map[string]string{"disabled_cloud9@123456789012": "x"}

	config := map[string]tftypes.Value{}
	for name, attr := range objType.AttributeTypes {
		config[name] = null(attr)
	}
	config["root_scope"] = str("aws/r-abcd")
	config["triggers"] = triggers(configured)

	proposed := map[string]tftypes.Value{}
	for k, v := range read {
		proposed[k] = v
	}
	proposed["triggers"] = triggers(configured)

	dv := func(vals map[string]tftypes.Value) *tfprotov6.DynamicValue {
		v, err := tfprotov6.NewDynamicValue(objType, tftypes.NewValue(objType, vals))
		if err != nil {
			t.Fatalf("NewDynamicValue: %v", err)
		}
		return &v
	}

	plan, err := srv.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "sonrai_control_submission",
		PriorState:       dv(read),
		ProposedNewState: dv(proposed),
		Config:           dv(config),
		PriorPrivate:     private,
	})
	if err != nil || hasErrors(plan.Diagnostics) {
		t.Fatalf("PlanResourceChange: err=%v diags=%v", err, plan.Diagnostics)
	}
	if checkPlanned != nil {
		planned, err := plan.PlannedState.Unmarshal(objType)
		if err != nil {
			t.Fatalf("Unmarshal planned state: %v", err)
		}
		var fields map[string]tftypes.Value
		if err := planned.As(&fields); err != nil {
			t.Fatalf("planned state: %v", err)
		}
		checkPlanned(fields)
	}
	for _, p := range plan.RequiresReplace {
		if p.Equal(tftypes.NewAttributePath().WithAttributeName("triggers")) {
			return true
		}
	}
	return false
}

func hasErrors(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

func TestImportedSubmissionAdoptsTriggers(t *testing.T) {
	// Replacing here would submit again before the imported transaction's
	// template_location reached the stack, which is the failure this avoids.
	if planTriggersAfterImport(t, true, nil, nil) {
		t.Error("first plan after import replaces the submission; it should adopt the triggers")
	}
}

func TestCreatedSubmissionStillReplacesOnTriggers(t *testing.T) {
	if !planTriggersAfterImport(t, false, nil, nil) {
		t.Error("adding triggers to a submission that was not imported should still submit again")
	}
}

func TestImportedSubmissionReplacesOnceTriggersRecorded(t *testing.T) {
	// The marker only covers triggers that were never recorded.
	if !planTriggersAfterImport(t, true, map[string]string{"older": "y"}, nil) {
		t.Error("changing recorded triggers should submit again even with the import marker present")
	}
}

// TestAdoptedSubmissionKeepsTemplateKnown: the stack's template_url comes from
// stack_template_location. If adopting triggers in place planned it as
// unknown, the stack would go from a null template_url to an unknown one, and
// the AWS provider fails that apply with "inconsistent final plan".
func TestAdoptedSubmissionKeepsTemplateKnown(t *testing.T) {
	planTriggersAfterImport(t, true, nil, func(planned map[string]tftypes.Value) {
		for _, name := range []string{"stack_template_location", "template_location", "transaction_id", "status"} {
			if !planned[name].IsKnown() {
				t.Errorf("%s is unknown in the plan; it should keep the value read from the platform", name)
			}
		}
	})
}
