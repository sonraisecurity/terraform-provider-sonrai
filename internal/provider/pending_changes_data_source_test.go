package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func pendingChangesSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	NewPendingChangesDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestPendingChangesAttributes(t *testing.T) {
	attrs := pendingChangesSchema(t).Attributes
	if attr, ok := attrs["root_scope"]; !ok || !attr.IsRequired() {
		t.Error("root_scope should be required")
	}
	for _, name := range []string{"id", "total", "changes"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
	nested := attrs["changes"].(schema.ListNestedAttribute).NestedObject.Attributes
	if len(nested) != len(pendingChangeAttrTypes) {
		t.Errorf("schema has %d change attributes, the object type has %d", len(nested), len(pendingChangeAttrTypes))
	}
	for name := range pendingChangeAttrTypes {
		if _, ok := nested[name]; !ok {
			t.Errorf("change attribute %q is in the object type but not the schema", name)
		}
	}
}

func TestPendingChangesTypeName(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	NewPendingChangesDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
	if want := "sonrai_pending_changes"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestProviderServesPendingChanges(t *testing.T) {
	ctx := context.Background()
	for _, factory := range New("test")().(*sonraiProvider).DataSources(ctx) {
		resp := &datasource.MetadataResponse{}
		factory().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		if resp.TypeName == "sonrai_pending_changes" {
			return
		}
	}
	t.Error("provider does not serve sonrai_pending_changes")
}

func TestPendingChangesWarningNamesWhatRidesAlong(t *testing.T) {
	changes := []client.PendingChange{
		{Name: "Deny access to Acme", ChangeType: "third_party_access"},
		{Name: "SONRAI UPDATE: Acme", ChangeType: "third_party_account"},
	}
	summary, detail := pendingChangesWarning("aws/r-abcd", changes)
	if !strings.Contains(summary, "2 changes") {
		t.Errorf("summary = %q, want the count", summary)
	}
	for _, want := range []string{"Deny access to Acme", "SONRAI UPDATE: Acme (queued by the platform)", "aws/r-abcd"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail is missing %q:\n%s", want, detail)
		}
	}
}

// TestPendingChangesWarningIsBounded: a root scope can carry thousands of
// pending changes, and a warning that lists them all buries the plan.
func TestPendingChangesWarningIsBounded(t *testing.T) {
	changes := make([]client.PendingChange, pendingChangesWarningLimit+7)
	for i := range changes {
		changes[i] = client.PendingChange{Name: "change", ChangeType: "mapping"}
	}
	_, detail := pendingChangesWarning("aws/r-abcd", changes)
	if got := strings.Count(detail, "  - "); got != pendingChangesWarningLimit {
		t.Errorf("listed %d changes, want %d", got, pendingChangesWarningLimit)
	}
	if !strings.Contains(detail, "and 7 more") {
		t.Errorf("detail should say how many were left out:\n%s", detail)
	}
}

func triggerMap(values map[string]attr.Value) types.Map {
	return types.MapValueMust(types.StringType, values)
}

// TestSubmissionReplaces pins when the pending-changes warning is raised for
// an existing submission. It has to agree with the plan modifiers that decide
// replacement, because the framework does not pass their answer on.
func TestSubmissionReplaces(t *testing.T) {
	root := types.StringValue("aws/r-abcd")
	recorded := triggerMap(map[string]attr.Value{"a": types.StringValue("r1")})
	cases := map[string]struct {
		state, plan controlSubmissionModel
		imported    bool
		want        bool
	}{
		"nothing changed": {
			state: controlSubmissionModel{RootScope: root, Triggers: recorded},
			plan:  controlSubmissionModel{RootScope: root, Triggers: recorded},
		},
		"trigger changed": {
			state: controlSubmissionModel{RootScope: root, Triggers: recorded},
			plan:  controlSubmissionModel{RootScope: root, Triggers: triggerMap(map[string]attr.Value{"a": types.StringValue("r2")})},
			want:  true,
		},
		"trigger unknown because its control will stage": {
			state: controlSubmissionModel{RootScope: root, Triggers: recorded},
			plan:  controlSubmissionModel{RootScope: root, Triggers: triggerMap(map[string]attr.Value{"a": types.StringUnknown()})},
			want:  true,
		},
		"root scope changed": {
			state: controlSubmissionModel{RootScope: root, Triggers: recorded},
			plan:  controlSubmissionModel{RootScope: types.StringValue("aws/r-zzzz"), Triggers: recorded},
			want:  true,
		},
		"imported submission adopting its triggers": {
			state:    controlSubmissionModel{RootScope: root, Triggers: types.MapNull(types.StringType)},
			plan:     controlSubmissionModel{RootScope: root, Triggers: recorded},
			imported: true,
		},
		"triggers added to a submission that was not imported": {
			state: controlSubmissionModel{RootScope: root, Triggers: types.MapNull(types.StringType)},
			plan:  controlSubmissionModel{RootScope: root, Triggers: recorded},
			want:  true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := submissionReplaces(tc.state, tc.plan, tc.imported); got != tc.want {
				t.Errorf("submissionReplaces = %v, want %v", got, tc.want)
			}
		})
	}
}
