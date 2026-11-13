package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

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
		if name != "sso" && !attr.IsRequired() {
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
		id       string
		identity string
		scope    string
		sso      bool
		wantErr  bool
	}{
		"arn":                  {id: "arn:aws:iam::*:role/BreakGlass@aws/r-abcd", identity: "arn:aws:iam::*:role/BreakGlass", scope: "aws/r-abcd"},
		"sso suffix":           {id: "someone@example.com@aws/r-abcd@sso", identity: "someone@example.com", scope: "aws/r-abcd", sso: true},
		"email-shaped non-sso": {id: "someone@example.com@aws/r-abcd", identity: "someone@example.com", scope: "aws/r-abcd"},
		"nested scope":         {id: "arn:a@aws/r-abcd/ou-1/123456789012", identity: "arn:a", scope: "aws/r-abcd/ou-1/123456789012"},
		"no separator":         {id: "arn:aws:iam::1:role/x", wantErr: true},
		"empty identity":       {id: "@aws/r-abcd", wantErr: true},
		"not a scope":          {id: "arn:a@not-a-scope", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			identity, scope, sso, err := parseExemptedIdentityImportID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parse(%q) succeeded, want error", tc.id)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.id, err)
			}
			if identity != tc.identity || scope != tc.scope || sso != tc.sso {
				t.Errorf("parse(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.id, identity, scope, sso, tc.identity, tc.scope, tc.sso)
			}
		})
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
