package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func customControlSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewCustomControlResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestCustomControlAttributes(t *testing.T) {
	attrs := customControlSchema(t).Attributes
	for _, name := range []string{"title", "cloud_type", "description", "permissions"} {
		if attr, ok := attrs[name]; !ok || !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	for _, name := range []string{"id", "control_key", "revision"} {
		attr, ok := attrs[name]
		if !ok || !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("%s should be computed-only", name)
		}
	}
	// A custom control belongs to the organization. Addressing it by scope
	// would suggest it could differ from one account to the next.
	for _, name := range []string{"scope", "root_scope", "resource_id"} {
		if _, ok := attrs[name]; ok {
			t.Errorf("%s does not belong on an organization-wide control", name)
		}
	}
}

func TestProviderServesCustomControls(t *testing.T) {
	ctx := context.Background()
	p := New("test")().(*sonraiProvider)
	found := false
	for _, factory := range p.Resources(ctx) {
		resp := &resource.MetadataResponse{}
		factory().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		found = found || resp.TypeName == "sonrai_custom_control"
	}
	if !found {
		t.Error("provider does not serve sonrai_custom_control")
	}
	found = false
	for _, factory := range p.DataSources(ctx) {
		resp := &datasource.MetadataResponse{}
		factory().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		found = found || resp.TypeName == "sonrai_custom_controls"
	}
	if !found {
		t.Error("provider does not serve sonrai_custom_controls")
	}
}

// TestCustomControlSchemaStatesWhatSurprises: an edit that does not take
// effect until redeployed, and a destroy that needs two applies, both have to
// be stated before anyone declares the resource.
func TestCustomControlSchemaStatesWhatSurprises(t *testing.T) {
	desc := customControlSchema(t).MarkdownDescription
	for _, want := range []string{
		"rejected rather than adopted",
		"needs a redeploy",
		"cannot be destroyed in the same apply that detaches it",
		"protects nothing",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("the schema description should mention %q", want)
		}
	}
}

func TestCustomControlTitlePattern(t *testing.T) {
	valid := []string{"Break Glass Actions", "bedrock-read", "s3.delete_bucket", "9lives"}
	invalid := []string{"", " leading", "-dash", "has:colon", "semi;colon", "slash/y", "new\nline", "quote\"d"}
	for _, title := range valid {
		if !customControlTitlePattern.MatchString(title) {
			t.Errorf("%q should be accepted", title)
		}
	}
	for _, title := range invalid {
		if customControlTitlePattern.MatchString(title) {
			t.Errorf("%q should be rejected", title)
		}
	}
}

func TestCustomControlPermissionPattern(t *testing.T) {
	valid := []string{"s3:DeleteBucket", "iam:*", "compute.googleapis.com/instances.delete"}
	invalid := []string{"", "s3: DeleteBucket", "s3:DeleteBucket,s3:PutObject", "s3:DeleteBucket\n", ":leading"}
	for _, p := range valid {
		if !customControlPermissionPattern.MatchString(p) {
			t.Errorf("%q should be accepted", p)
		}
	}
	for _, p := range invalid {
		if customControlPermissionPattern.MatchString(p) {
			t.Errorf("%q should be rejected", p)
		}
	}
}

func TestKeepSpelling(t *testing.T) {
	got := keepSpelling(
		[]string{"s3:DeleteBucket", "s3:DeleteBucketPolicy", "iam:CreateUser"},
		[]string{"s3:deletebucket", "S3:DELETEBUCKETPOLICY", "ec2:Gone"},
	)
	// Written spellings survive where they name the same permission; one the
	// platform added comes through as the platform has it; one it dropped is
	// not carried over.
	want := []string{"s3:deletebucket", "S3:DELETEBUCKETPOLICY", "iam:CreateUser"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDiagnoseCustomControlErrorOffersImport(t *testing.T) {
	m := customControlModel{Title: types.StringValue("Break Glass"), CloudType: types.StringValue("aws")}
	diags := diagnoseCustomControlError("Unable to create custom control", m, true,
		errors.New("a control already exists with key 'breakglass' for cloud type aws - use createOnly=false to update it"))
	if !diags.HasError() {
		t.Fatal("expected an error")
	}
	detail := diags[0].Detail()
	if !strings.Contains(detail, "terraform import <address> 'aws:breakglass'") {
		t.Errorf("detail should give the import command: %s", detail)
	}
	// The platform's own advice is to overwrite, which is the one thing this
	// resource must not suggest.
	if strings.Contains(detail, "createOnly") {
		t.Errorf("detail should not pass on the platform's suggestion to overwrite: %s", detail)
	}
}

func TestSyncRequiredDetailNamesEveryRootScope(t *testing.T) {
	detail := syncRequiredDetail("breakglass", []client.CustomControlSync{
		{RootScope: "aws/r-zzzz", Scopes: []string{"a"}},
		{RootScope: "aws/r-abcd", Scopes: []string{"a", "b"}},
	}, true)
	for _, want := range []string{"aws/r-abcd (2 scopes)", "aws/r-zzzz (1 scope)", "old list stays in force", "in this apply"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail is missing %q:\n%s", want, detail)
		}
	}
}

func TestStillDeployedDetailSaysItTakesTwoApplies(t *testing.T) {
	detail := stillDeployedDetail("breakglass", []string{"aws/r-abcd/1", "aws/r-abcd/2"})
	for _, want := range []string{"aws/r-abcd/1", "aws/r-abcd/2", "two applies", "Nothing has been changed"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail is missing %q:\n%s", want, detail)
		}
	}
}
