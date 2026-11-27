package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func approversSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewApproversResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestApproversAttributes(t *testing.T) {
	attrs := approversSchema(t).Attributes
	for _, name := range []string{"root_scope", "resource_id", "approvers", "approval_type"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	for _, name := range []string{"id", "scope", "previous_approval_type"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("%s should be computed-only", name)
		}
	}
}

// TestApproversHasNoRevision: approvers take effect immediately. A revision
// would invite wiring them into a submission's triggers, and every approver
// change would then submit whatever happened to be pending.
func TestApproversHasNoRevision(t *testing.T) {
	if _, ok := approversSchema(t).Attributes["revision"]; ok {
		t.Error("sonrai_approvers stages nothing and must not expose a revision")
	}
}

func TestApproversTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewApproversResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
	if want := "sonrai_approvers"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestProviderServesApprovers(t *testing.T) {
	ctx := context.Background()
	for _, factory := range New("test")().(*sonraiProvider).Resources(ctx) {
		resp := &resource.MetadataResponse{}
		factory().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		if resp.TypeName == "sonrai_approvers" {
			return
		}
	}
	t.Error("provider does not serve sonrai_approvers")
}

// TestApproversSchemaStatesItsSideEffects: applying this resource sends
// invites and grants a role, and leaves exceptions alone. Each has to be
// stated before anyone declares it.
func TestApproversSchemaStatesItsSideEffects(t *testing.T) {
	desc := approversSchema(t).MarkdownDescription
	for _, want := range []string{
		"exceptions back unchanged",
		"sent an invite",
		"CloudPermissionFirewallApprover",
		"rejected rather than adopted",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("the schema description should mention %q", want)
		}
	}
}

func TestApproverEmailPattern(t *testing.T) {
	valid := []string{"amy@example.com", "first.last+tag@sub.example.co.uk", "o'brien@example.com", "cloud-approvers@example.io"}
	invalid := []string{
		"", "amy", "amy@", "@example.com", "amy@example", "Amy@example.com", "amy@Example.com",
		"amy @example.com", "amy@example.com ", "amy@exa mple.com", "amy@-example.com", "amy@example..com",
		"amy@example.com,bob@example.com", "amy@example.com\n", "srn:supersonrai::User/1",
	}
	for _, email := range valid {
		if !approverEmailPattern.MatchString(email) {
			t.Errorf("%q should be accepted", email)
		}
	}
	for _, email := range invalid {
		if approverEmailPattern.MatchString(email) {
			t.Errorf("%q should be rejected", email)
		}
	}
}

func TestApproverScopePattern(t *testing.T) {
	valid := []string{
		"aws/r-abcd", "aws/r-abcd/ou-ab12-cdef3456", "aws/r-abcd/ou-ab12-cdef3456/123456789012",
		"gcp/organizations|123/folders|456/projects|my-project", "azure/tenant-id/|subscriptions|0000-11",
	}
	invalid := []string{
		"", "aws", "aws/", "aws/r-abcd/", "aws//123456789012", "r-abcd/123456789012",
		"aws/r-abcd /123456789012", "aws/r-abcd/%", "oci/tenancy", "aws/r-abcd\n",
	}
	for _, scope := range valid {
		if !approverScopePattern.MatchString(scope) {
			t.Errorf("%q should be accepted", scope)
		}
	}
	for _, scope := range invalid {
		if approverScopePattern.MatchString(scope) {
			t.Errorf("%q should be rejected", scope)
		}
	}
}

func TestHierarchyResourceIDPattern(t *testing.T) {
	valid := []string{"123456789012", "ou-ab12-cdef3456", "r-abcd", "projects|my-project", "folders/456"}
	// A "@" would split an import id in the wrong place.
	invalid := []string{"", " ", "1234 5678", "amy@example.com", "aws/r-abcd@123456789012", "123456789012\n"}
	for _, id := range valid {
		if !hierarchyResourceIDPattern.MatchString(id) {
			t.Errorf("%q should be accepted", id)
		}
	}
	for _, id := range invalid {
		if hierarchyResourceIDPattern.MatchString(id) {
			t.Errorf("%q should be rejected", id)
		}
	}
}

func TestApproversID(t *testing.T) {
	m := approversModel{RootScope: types.StringValue("aws/r-abcd"), ResourceID: types.StringValue("ou-ab12-cdef3456")}
	if got, want := approversID(m), "aws/r-abcd@ou-ab12-cdef3456"; got != want {
		t.Errorf("approversID = %q, want %q", got, want)
	}
}

func TestApprovalTypeNamesRoundTrip(t *testing.T) {
	names := approvalTypeNames()
	if len(names) != len(client.ApprovalTypes) {
		t.Fatalf("got %d names for %d API types", len(names), len(client.ApprovalTypes))
	}
	for i, api := range client.ApprovalTypes {
		if names[i] != strings.ToLower(api) || approvalTypeAPI(names[i]) != api {
			t.Errorf("%q does not round trip through %q", api, names[i])
		}
	}
}

func TestWarnExceptionsRemoved(t *testing.T) {
	var none diag.Diagnostics
	warnExceptionsRemoved(&none, "aws/r-abcd", 0)
	if len(none) != 0 {
		t.Error("no exceptions lost should produce no warning")
	}
	// More exceptions after the write than before means someone added one.
	warnExceptionsRemoved(&none, "aws/r-abcd", -1)
	if len(none) != 0 {
		t.Error("a gain in exceptions should produce no warning")
	}

	var some diag.Diagnostics
	warnExceptionsRemoved(&some, "aws/r-abcd", 2)
	if some.WarningsCount() != 1 || some.HasError() {
		t.Fatalf("want exactly one warning, got %v", some)
	}
	if summary := some[0].Summary(); !strings.Contains(summary, "2 approver exceptions were removed from aws/r-abcd") {
		t.Errorf("summary = %q", summary)
	}
}
