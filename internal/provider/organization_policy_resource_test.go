package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func resourceSchema(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestOrganizationPolicyAttributes(t *testing.T) {
	attrs := resourceSchema(t, NewOrganizationPolicyResource()).Attributes
	for _, name := range []string{"root_scope", "name", "policy_type", "content"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	if attr := attrs["description"]; attr == nil || !attr.IsOptional() || attr.IsComputed() {
		t.Error("description should be optional and not computed")
	}
	for _, name := range []string{"id", "revision"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("%s should be computed-only", name)
		}
	}
	// The policy is not at a scope; its attachments are.
	for _, name := range []string{"scope", "resource_id"} {
		if _, ok := attrs[name]; ok {
			t.Errorf("%s belongs on the attachment, not the policy", name)
		}
	}
}

func TestOrganizationPolicyAttachmentAttributes(t *testing.T) {
	attrs := resourceSchema(t, NewOrganizationPolicyAttachmentResource()).Attributes
	for _, name := range []string{"policy_id", "root_scope", "resource_id"} {
		attr, ok := attrs[name].(schema.StringAttribute)
		if !ok || !attr.IsRequired() {
			t.Errorf("%s should be a required string", name)
			continue
		}
		// Nothing about an attachment can be changed in place: a different
		// policy or target is a different attachment.
		if len(attr.PlanModifiers) != 1 {
			t.Errorf("%s should force replacement", name)
		}
	}
	for _, name := range []string{"id", "scope", "revision"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("%s should be computed-only", name)
		}
	}
}

func TestProviderServesOrganizationPolicies(t *testing.T) {
	ctx := context.Background()
	p := New("test")().(*sonraiProvider)
	resources := map[string]bool{}
	for _, factory := range p.Resources(ctx) {
		resp := &resource.MetadataResponse{}
		factory().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		resources[resp.TypeName] = true
	}
	for _, name := range []string{"sonrai_organization_policy", "sonrai_organization_policy_attachment"} {
		if !resources[name] {
			t.Errorf("provider does not serve %s", name)
		}
	}
	found := false
	for _, factory := range p.DataSources(ctx) {
		resp := &datasource.MetadataResponse{}
		factory().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		found = found || resp.TypeName == "sonrai_organization_policies"
	}
	if !found {
		t.Error("provider does not serve sonrai_organization_policies")
	}
}

// TestOrganizationPolicySchemasStateWhatSurprises: a destroy that needs two
// applies, an edit that waits for a
// redeploy, and an attachment that cannot be told from inheritance all have to
// be stated before anyone declares the resource.
func TestOrganizationPolicySchemasStateWhatSurprises(t *testing.T) {
	policy := resourceSchema(t, NewOrganizationPolicyResource()).MarkdownDescription
	for _, want := range []string{
		"rejected rather than adopted",
		"needs a redeploy",
		"cannot be destroyed in the same apply that detaches it",
		"in force nowhere",
		"does not keep policy names unique",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy description should mention %q", want)
		}
	}
	attachment := resourceSchema(t, NewOrganizationPolicyAttachmentResource()).MarkdownDescription
	for _, want := range []string{
		"rejected rather than adopted",
		"cannot be told apart from inheritance",
		"submitted and deployed",
	} {
		if !strings.Contains(attachment, want) {
			t.Errorf("the attachment description should mention %q", want)
		}
	}
}

func TestPolicyNamePattern(t *testing.T) {
	valid := []string{"deny-delete", "Deny S3 Delete", "p", "a.b_c (prod)"}
	invalid := []string{"", " leading", "trailing ", "new\nline", "tab\there", "\x00"}
	for _, name := range valid {
		if !policyNamePattern.MatchString(name) {
			t.Errorf("%q should be accepted", name)
		}
	}
	for _, name := range invalid {
		if policyNamePattern.MatchString(name) {
			t.Errorf("%q should be rejected", name)
		}
	}
}

func TestReservedPolicyNameIsRefused(t *testing.T) {
	for name, wantError := range map[string]bool{"SonraiSCP": true, "Sonrai": true, "sonrai-ish": false, "MySonrai": false} {
		resp := &validator.StringResponse{}
		notReservedPolicyName{}.ValidateString(context.Background(), validator.StringRequest{
			Path: path.Root("name"), ConfigValue: types.StringValue(name),
		}, resp)
		if resp.Diagnostics.HasError() != wantError {
			t.Errorf("%q: error = %v, want %v", name, resp.Diagnostics.HasError(), wantError)
		}
	}
}

func TestPolicyDocumentProblem(t *testing.T) {
	ok := []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:*","Resource":"*"}]}`,
		`{"Statement":{"Effect":"Deny","Action":"s3:*","Resource":"*"}}`,
	}
	for _, content := range ok {
		if problem := policyDocumentProblem(content); problem != "" {
			t.Errorf("%s: unexpected problem %q", content, problem)
		}
	}
	bad := map[string]string{
		`not json`:                 "JSON object",
		`["a"]`:                    "JSON object",
		`{"Version":"2012-10-17"}`: `no "Statement"`,
		`{"Statement":[]}`:         "at least one statement",
		`{"Statement":"Deny"}`:     "a statement or a list",
	}
	for content, want := range bad {
		if problem := policyDocumentProblem(content); !strings.Contains(problem, want) {
			t.Errorf("%s: problem = %q, want one mentioning %q", content, problem, want)
		}
	}
}

// TestSameDocumentIgnoresFormatting: the platform stores the document
// compacted, so a comparison of the text would show a change on every plan.
func TestSameDocumentIgnoresFormatting(t *testing.T) {
	written := newPolicyDocument("{\n  \"Statement\": [ {\"Effect\": \"Deny\", \"Action\": \"s3:*\"} ],\n  \"Version\": \"2012-10-17\"\n}")
	stored := newPolicyDocument(`{"Version":"2012-10-17","Statement":[{"Action":"s3:*","Effect":"Deny"}]}`)
	edited := newPolicyDocument(`{"Version":"2012-10-17","Statement":[{"Action":"s3:Get*","Effect":"Deny"}]}`)
	if !sameDocument(written, stored) {
		t.Error("the same document in different formatting should compare equal")
	}
	if sameDocument(written, edited) {
		t.Error("a different action should compare unequal")
	}
	if sameDocument(written, newPolicyDocumentNull()) {
		t.Error("a document should not equal null")
	}
}

// TestRootScopeDoesNotReplaceAfterImport: the platform cannot report the root
// scope, so the first plan after an import sets it from configuration. Reading
// that as a change would replace a policy that is deployed.
func TestRootScopeDoesNotReplaceAfterImport(t *testing.T) {
	cases := map[string]struct {
		state types.String
		want  bool
	}{
		"imported": {types.StringNull(), false},
		"changed":  {types.StringValue("aws/r-abcd"), true},
	}
	for name, tc := range cases {
		resp := &stringplanmodifier.RequiresReplaceIfFuncResponse{}
		replaceUnlessImported(context.Background(), planmodifier.StringRequest{
			StateValue: tc.state, PlanValue: types.StringValue("aws/r-wxyz"),
		}, resp)
		if resp.RequiresReplace != tc.want {
			t.Errorf("%s: RequiresReplace = %v, want %v", name, resp.RequiresReplace, tc.want)
		}
	}
}

func TestPolicyStillDeployedDetailSaysItTakesTwoApplies(t *testing.T) {
	detail := policyStillDeployedDetail("deny-delete", []string{"aws/r-abcd/ou-1", "aws/r-abcd/222"})
	for _, want := range []string{"deny-delete", "aws/r-abcd/ou-1", "aws/r-abcd/222", "two applies", "Nothing has been changed"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail should mention %q:\n%s", want, detail)
		}
	}
}

func TestDiagnoseOrganizationPolicyErrorExplainsOpaqueRefusals(t *testing.T) {
	cases := map[string]string{
		"Received an unknown or internal gRPC error. Request ID: 1":                                               "without giving a reason",
		"UNIMPLEMENTED: organization policies are disabled":                                                       "not enabled",
		"PermissionDenied: organization policy \"p\" is attached or staged outside the scopes you have access to": "not permitted",
		"something else": "something else",
	}
	for message, want := range cases {
		diags := diagnoseOrganizationPolicyError("Unable to update", errors.New(message))
		if len(diags) != 1 || !strings.Contains(diags[0].Summary()+" "+diags[0].Detail(), want) {
			t.Errorf("%q: diagnostics = %v, want one mentioning %q", message, diags, want)
		}
	}
	diags := diagnoseOrganizationPolicyError("Unable to update", client.ErrUnauthorized)
	if len(diags) != 1 || !strings.Contains(diags[0].Summary(), "rejected the token") {
		t.Errorf("unauthorized: diagnostics = %v", diags)
	}
}

func TestAttachmentIDRoundTripsThroughImport(t *testing.T) {
	m := organizationPolicyAttachmentModel{
		PolicyID:   types.StringValue("a1b2c3d4-0000-4000-8000-000000000000"),
		RootScope:  types.StringValue("aws/r-abcd"),
		ResourceID: types.StringValue("ou-abcd-1a2b3c"),
	}
	id := attachmentID(m)
	if id != "a1b2c3d4-0000-4000-8000-000000000000@aws/r-abcd@ou-abcd-1a2b3c" {
		t.Fatalf("attachmentID = %q", id)
	}
	for _, bad := range []string{
		"", "a1b2c3d4-0000-4000-8000-000000000000", "nope@aws/r-abcd@123456789012",
		"a1b2c3d4-0000-4000-8000-000000000000@gcp/organizations|1@p",
		"a1b2c3d4-0000-4000-8000-000000000000@aws/r-abcd/ou-1@123456789012",
		"a1b2c3d4-0000-4000-8000-000000000000@aws/r-abcd@",
	} {
		resp := &resource.ImportStateResponse{}
		NewOrganizationPolicyAttachmentResource().(resource.ResourceWithImportState).ImportState(
			context.Background(), resource.ImportStateRequest{ID: bad}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import id %q should be refused", bad)
		}
	}
}

func TestSameJSON(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, "{ \"b\": [1, 2],\n \"a\": 1 }", true},
		{`{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{`{"a":1}`, `{"a":1.0}`, false},
		{`{"a":9007199254740993}`, `{"a":9007199254740992}`, false},
		{`not json`, `not json`, true},
		{`not json`, `{"a":1}`, false},
		{`{"a":1} trailing`, `{"a":1}`, false},
	}
	for _, tc := range cases {
		if got := sameJSON(tc.a, tc.b); got != tc.want {
			t.Errorf("sameJSON(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestScopeResourceID: the id has to be one a resource accepts as resource_id
// unchanged, including the root's own id and the qualified forms GCP and
// Azure store.
func TestScopeResourceID(t *testing.T) {
	cases := map[string]string{
		"aws/r-abcd":                              "r-abcd",
		"aws/r-abcd/ou-abcd-1a2b3c":               "ou-abcd-1a2b3c",
		"aws/r-abcd/ou-abcd-1a2b3c/123456789012":  "123456789012",
		"gcp/organizations|1/projects|my-project": "projects|my-project",
		"azure/|tenants|x/|subscriptions|a1b2":    "|subscriptions|a1b2",
		"aws":                                     "",
		"":                                        "",
	}
	for scope, want := range cases {
		if got := scopeResourceID(scope); got != want {
			t.Errorf("scopeResourceID(%q) = %q, want %q", scope, got, want)
		}
	}
	ids := scopeResourceIDs([]string{"aws/r-abcd/111111111111", "aws/r-abcd"})
	if len(ids) != 2 || ids[0] != "111111111111" || ids[1] != "r-abcd" {
		t.Errorf("scopeResourceIDs = %v, want the ids in the order of the scopes", ids)
	}
}

func TestDataSourcesReportResourceIDsBesideScopes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		source      datasource.DataSource
		list, field string
	}{
		{NewCustomControlsDataSource(), "controls", "deployed_resource_ids"},
		{NewOrganizationPoliciesDataSource(), "policies", "attached_resource_ids"},
		{NewPendingChangesDataSource(), "changes", "resource_id"},
	}
	for _, tc := range cases {
		resp := &datasource.SchemaResponse{}
		tc.source.Schema(ctx, datasource.SchemaRequest{}, resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Fatalf("%s: invalid schema: %s", tc.list, diags)
		}
		list, ok := resp.Schema.Attributes[tc.list].(dsschema.ListNestedAttribute)
		if !ok {
			t.Fatalf("%s is not a nested list", tc.list)
		}
		if attr, ok := list.NestedObject.Attributes[tc.field]; !ok || !attr.IsComputed() {
			t.Errorf("%s should report a computed %s", tc.list, tc.field)
		}
	}
}

// TestRevisionAfterEdit: a revision the plan pinned has to come back
// unchanged, or Terraform fails the apply with an inconsistent result. The
// redeploy is then left to the next refresh.
func TestRevisionAfterEdit(t *testing.T) {
	prior := types.StringValue("prior")
	pinned := types.StringValue("pinned")

	got, submitsNow := revisionAfterEdit(pinned, prior, true)
	if !got.Equal(pinned) || submitsNow {
		t.Errorf("pinned, needs deploy: revision = %s, submitsNow = %v; want the pinned value and false", got, submitsNow)
	}
	got, submitsNow = revisionAfterEdit(pinned, prior, false)
	if !got.Equal(pinned) || submitsNow {
		t.Errorf("pinned, no deploy: revision = %s, submitsNow = %v", got, submitsNow)
	}
	got, submitsNow = revisionAfterEdit(types.StringUnknown(), prior, true)
	if got.IsUnknown() || got.IsNull() || got.Equal(prior) || !submitsNow {
		t.Errorf("unknown, needs deploy: revision = %s, submitsNow = %v; want a new value and true", got, submitsNow)
	}
	got, submitsNow = revisionAfterEdit(types.StringUnknown(), prior, false)
	if !got.Equal(prior) || submitsNow {
		t.Errorf("unknown, no deploy: revision = %s, submitsNow = %v; want the prior value and false", got, submitsNow)
	}
}

func TestRedeployWarningSaysWhichApplySubmits(t *testing.T) {
	roots := []string{"aws/r-abcd"}
	sync := []client.CustomControlSync{{RootScope: "aws/r-abcd", Scopes: []string{"a"}}}
	for name, now := range map[string]string{
		"policy":  policyRedeployDetail("deny-delete", roots, true),
		"control": syncRequiredDetail("breakglass", sync, true),
	} {
		if !strings.Contains(now, "does that in this apply") || strings.Contains(now, "apply again") {
			t.Errorf("%s: a scheduled submission should be promised for this apply:\n%s", name, now)
		}
	}
	for name, later := range map[string]string{
		"policy":  policyRedeployDetail("deny-delete", roots, false),
		"control": syncRequiredDetail("breakglass", sync, false),
	} {
		if strings.Contains(later, "does that in this apply") || !strings.Contains(later, "apply again") {
			t.Errorf("%s: an unscheduled submission must not be promised for this apply:\n%s", name, later)
		}
		if !strings.Contains(later, "aws/r-abcd") {
			t.Errorf("%s: detail should still name the root scope:\n%s", name, later)
		}
	}
}

func TestPolicyDetachStagedDetail(t *testing.T) {
	detail := policyDetachStagedDetail("deny-delete", "aws/r-abcd/ou-1")
	for _, want := range []string{"deny-delete", "aws/r-abcd/ou-1", "staged for removal", "remains in force"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail should mention %q:\n%s", want, detail)
		}
	}
}
