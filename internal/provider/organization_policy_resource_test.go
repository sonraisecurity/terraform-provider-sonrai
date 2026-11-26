package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
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

// TestOrganizationPolicySchemasStateWhatSurprises: a destroy that reaches
// attachments this configuration never made, an edit that waits for a
// redeploy, and an attachment that cannot be told from inheritance all have to
// be stated before anyone declares the resource.
func TestOrganizationPolicySchemasStateWhatSurprises(t *testing.T) {
	policy := resourceSchema(t, NewOrganizationPolicyResource()).MarkdownDescription
	for _, want := range []string{
		"rejected rather than adopted",
		"needs a redeploy",
		"removes it everywhere",
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
	written := jsontypes.NewNormalizedValue("{\n  \"Statement\": [ {\"Effect\": \"Deny\", \"Action\": \"s3:*\"} ],\n  \"Version\": \"2012-10-17\"\n}")
	stored := jsontypes.NewNormalizedValue(`{"Version":"2012-10-17","Statement":[{"Action":"s3:*","Effect":"Deny"}]}`)
	edited := jsontypes.NewNormalizedValue(`{"Version":"2012-10-17","Statement":[{"Action":"s3:Get*","Effect":"Deny"}]}`)
	if !sameDocument(written, stored) {
		t.Error("the same document in different formatting should compare equal")
	}
	if sameDocument(written, edited) {
		t.Error("a different action should compare unequal")
	}
	if sameDocument(written, jsontypes.NewNormalizedNull()) {
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

func TestDeployedDestroyDetailNamesScopesAndRootScopes(t *testing.T) {
	detail := deployedDestroyDetail("deny-delete", []string{"aws/r-abcd/ou-1", "aws/r-wxyz"}, []string{"aws/r-abcd", "aws/r-wxyz"})
	for _, want := range []string{"deny-delete", "aws/r-abcd/ou-1", "whether or not this configuration", "aws/r-abcd, aws/r-wxyz", "stays in force"} {
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
