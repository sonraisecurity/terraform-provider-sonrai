package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func configureProvider(t *testing.T) *provider.ConfigureResponse {
	t.Helper()
	ctx := context.Background()

	t.Setenv("SONRAI_ORG", "example")
	t.Setenv("SONRAI_TOKEN", "token")
	t.Setenv("SONRAI_DOMAIN", "")

	p := New("test")()
	schemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", schemaResp.Diagnostics)
	}

	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"org":    tftypes.NewValue(tftypes.String, nil),
		"domain": tftypes.NewValue(tftypes.String, nil),
		"token":  tftypes.NewValue(tftypes.String, nil),
	})

	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, provider.ConfigureRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: raw},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("configure diagnostics: %s", resp.Diagnostics)
	}
	return resp
}

// TestConfigurePopulatesEveryConsumer pins both handoffs. A consumer that
// receives no client holds a nil one that only fails once used, as a panic.
func TestConfigurePopulatesEveryConsumer(t *testing.T) {
	resp := configureProvider(t)

	for name, data := range map[string]any{
		"ResourceData":   resp.ResourceData,
		"DataSourceData": resp.DataSourceData,
	} {
		if data == nil {
			t.Errorf("%s is nil, so its consumers start unconfigured", name)
		}
	}
}

// TestProviderServesNoActions pins the absence. Actions are a Terraform 1.14
// feature that OpenTofu does not implement, so everything this provider does
// has to be reachable as a resource or a data source.
func TestProviderServesNoActions(t *testing.T) {
	if _, ok := New("test")().(provider.ProviderWithActions); ok {
		t.Error("the provider serves actions, which OpenTofu cannot invoke")
	}
}
