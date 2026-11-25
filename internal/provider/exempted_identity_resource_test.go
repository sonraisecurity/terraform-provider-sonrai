package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func exemptedIdentitySchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewExemptedIdentityResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestExemptedIdentitySchemaIsValid(t *testing.T) {
	exemptedIdentitySchema(t)
}

func TestExemptedIdentityTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewExemptedIdentityResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
	if want := "sonrai_exempted_identity"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

// TestExemptedIdentityIdentityForcesReplacement: the platform allows one entry
// per identity at a scope and keys it on both, so changing either has to be a
// replace rather than an update.
func TestExemptedIdentityReplaceOnIdentityAndScope(t *testing.T) {
	attrs := exemptedIdentitySchema(t).Attributes
	for _, name := range []string{"identity", "scope", "sso"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing %s attribute", name)
		}
		if name == "identity" && !attr.IsRequired() {
			t.Errorf("%s should be required", name)
		}
	}
	if exemptFrom, ok := attrs["exempt_from"]; !ok || !exemptFrom.IsRequired() {
		t.Error("exempt_from should be required")
	}
}

// TestExemptionTypeNamesCoverTheAPI: a type the API supports but the mapping
// omits cannot be expressed in configuration, and one the mapping invents is
// silently dropped on the way out.
func TestExemptionTypeNamesCoverTheAPI(t *testing.T) {
	if len(exemptionTypeNames) != len(client.ExemptionTypes) {
		t.Fatalf("mapping has %d types, API has %d", len(exemptionTypeNames), len(client.ExemptionTypes))
	}
	seen := map[string]bool{}
	for _, api := range exemptionTypeNames {
		seen[api] = true
	}
	for _, api := range client.ExemptionTypes {
		if !seen[api] {
			t.Errorf("no configuration name maps to API type %q", api)
		}
	}
}

func TestExemptionNamesRoundTrip(t *testing.T) {
	cfg := exemptionTypeKeys()
	got := configNames(apiNames(cfg))
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("round trip = %v, want %v", got, cfg)
	}
}

func TestProviderServesTheExemptedIdentityResource(t *testing.T) {
	ctx := context.Background()
	for _, factory := range New("test")().(*sonraiProvider).Resources(ctx) {
		resp := &resource.MetadataResponse{}
		factory().Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)
		if resp.TypeName == "sonrai_exempted_identity" {
			return
		}
	}
	t.Error("provider does not serve sonrai_exempted_identity")
}

func TestParseExemptedIdentityImportID(t *testing.T) {
	cases := map[string]struct {
		id      string
		want    exemptedIdentityImport
		wantErr bool
	}{
		"by id":           {id: "*e/BreakGlass@aws/r-abcd@r-abcd", want: exemptedIdentityImport{identity: "*e/BreakGlass", rootScope: "aws/r-abcd", resourceID: "r-abcd"}},
		"by id, account":  {id: "arn:a@aws/r-abcd@123456789012", want: exemptedIdentityImport{identity: "arn:a", rootScope: "aws/r-abcd", resourceID: "123456789012"}},
		"by id, sso":      {id: "someone@example.com@aws/r-abcd@123456789012@sso", want: exemptedIdentityImport{identity: "someone@example.com", rootScope: "aws/r-abcd", resourceID: "123456789012", sso: true}},
		"by id, email":    {id: "someone@example.com@aws/r-abcd@ou-ab12-cdef3456", want: exemptedIdentityImport{identity: "someone@example.com", rootScope: "aws/r-abcd", resourceID: "ou-ab12-cdef3456"}},
		"path":            {id: "arn:aws:iam::*:role/BreakGlass@aws/r-abcd", want: exemptedIdentityImport{identity: "arn:aws:iam::*:role/BreakGlass", scope: "aws/r-abcd"}},
		"path, sso":       {id: "someone@example.com@aws/r-abcd@sso", want: exemptedIdentityImport{identity: "someone@example.com", scope: "aws/r-abcd", sso: true}},
		"path, email":     {id: "someone@example.com@aws/r-abcd", want: exemptedIdentityImport{identity: "someone@example.com", scope: "aws/r-abcd"}},
		"path, nested":    {id: "arn:a@aws/r-abcd/ou-1/123456789012", want: exemptedIdentityImport{identity: "arn:a", scope: "aws/r-abcd/ou-1/123456789012"}},
		"no separator":    {id: "arn:aws:iam::1:role/x", wantErr: true},
		"empty identity":  {id: "@aws/r-abcd", wantErr: true},
		"not a scope":     {id: "arn:a@not-a-scope", wantErr: true},
		"id without root": {id: "arn:a@123456789012", wantErr: true},
		"empty id":        {id: "arn:a@aws/r-abcd@", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseExemptedIdentityImportID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parse(%q) = %+v, want error", tc.id, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.id, err)
			}
			if got != tc.want {
				t.Errorf("parse(%q) = %+v, want %+v", tc.id, got, tc.want)
			}
		})
	}
}

// TestExemptedIdentityImportIDFollowsTheConfiguration: the id suggested when
// an exemption already exists has to be one the configuration can keep, in
// the form it names its scope.
func TestExemptedIdentityImportIDFollowsTheConfiguration(t *testing.T) {
	byID := exemptedIdentityModel{
		Identity:   types.StringValue("someone@example.com"),
		Scope:      types.StringValue("aws/r-abcd/ou-1/123456789012"),
		RootScope:  types.StringValue("aws/r-abcd"),
		ResourceID: types.StringValue("123456789012"),
		SSO:        types.BoolValue(true),
	}
	if got, want := exemptedIdentityImportID(byID), "someone@example.com@aws/r-abcd@123456789012@sso"; got != want {
		t.Errorf("by id: %q, want %q", got, want)
	}
	byPath := exemptedIdentityModel{
		Identity: types.StringValue("*e/BreakGlass"),
		Scope:    types.StringValue("aws/r-abcd"),
		SSO:      types.BoolValue(false),
	}
	if got, want := exemptedIdentityImportID(byPath), "*e/BreakGlass@aws/r-abcd"; got != want {
		t.Errorf("by path: %q, want %q", got, want)
	}
	for _, id := range []string{exemptedIdentityImportID(byID), exemptedIdentityImportID(byPath)} {
		if _, err := parseExemptedIdentityImportID(id); err != nil {
			t.Errorf("%q does not parse back: %v", id, err)
		}
	}
}

// TestScopeIsNamedByIDOrPath: scope has to stay settable for configurations
// written against 0.1, so none of the three can be required.
func TestScopeIsNamedByIDOrPath(t *testing.T) {
	attrs := exemptedIdentitySchema(t).Attributes
	if scope := attrs["scope"]; !scope.IsOptional() || !scope.IsComputed() {
		t.Error("scope must be optional and computed")
	}
	for _, name := range []string{"root_scope", "resource_id"} {
		if attr, ok := attrs[name]; !ok || !attr.IsOptional() || attr.IsComputed() {
			t.Errorf("%s should be optional and not computed", name)
		}
	}
	if got := len(NewExemptedIdentityResource().(resource.ResourceWithConfigValidators).ConfigValidators(context.Background())); got != 2 {
		t.Errorf("want 2 config validators, got %d", got)
	}
}

// TestExemptedIdentityRejectsAdoption pins the rule the granular control
// resources already follow. Adopting an exemption someone else made would have
// destroy remove it, and nothing in the configuration would say why it existed.
func TestExemptedIdentityRejectsAdoption(t *testing.T) {
	desc := exemptedIdentitySchema(t).MarkdownDescription
	if !strings.Contains(desc, "rejected rather than adopted") {
		t.Error("the schema should state that an existing exemption is rejected, not adopted")
	}
}

// TestJitConfigurationWarnsItReplaces: the resource is authoritative for its
// whole scope, so a first apply discards whatever enrolment the scope already
// has. The schema has to say so before anyone declares it.
func TestJitConfigurationWarnsItReplaces(t *testing.T) {
	desc := jitSchema(t).MarkdownDescription
	if !strings.Contains(desc, "replaces whatever the scope already has") {
		t.Error("the schema should warn that declaring the resource replaces the scope's configuration")
	}
}

// TestExemptedIdentityStatesExemptFromIsTheWholeList: the creation call only
// adds to a deployed exemption, so the schema has to promise that taking a
// kind out of the list removes it.
func TestExemptedIdentityStatesExemptFromIsTheWholeList(t *testing.T) {
	desc := exemptedIdentitySchema(t).MarkdownDescription
	for _, want := range []string{"complete list", "is removed when the change is deployed"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the schema description should mention %q", want)
		}
	}
}
