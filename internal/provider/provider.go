// Package provider implements the Terraform provider for Sonrai Security.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var _ provider.Provider = (*sonraiProvider)(nil)

type sonraiProvider struct {
	version string
}

// New returns a provider factory for the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &sonraiProvider{version: version}
	}
}

func (p *sonraiProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "sonrai"
	resp.Version = p.version
}

type providerModel struct {
	Org    types.String `tfsdk:"org"`
	Domain types.String `tfsdk:"domain"`
	Token  types.String `tfsdk:"token"`
}

func (p *sonraiProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Sonrai Security platform configuration through the Sonrai GraphQL API.",
		Attributes: map[string]schema.Attribute{
			"org": schema.StringAttribute{
				MarkdownDescription: "Sonrai organization name, the first label of your console hostname. May also be set with `SONRAI_ORG`; a value set here takes precedence over it.",
				Optional:            true,
			},
			"domain": schema.StringAttribute{
				MarkdownDescription: "Sonrai console domain. May also be set with `SONRAI_DOMAIN`; a value set here takes precedence over it. Defaults to `sonraisecurity.com`.",
				Optional:            true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "Bearer token for the Sonrai API, issued to an App identity. May also be set with `SONRAI_TOKEN`, which is the preferred way to supply it: a token written into configuration is also written into state, and takes precedence over the environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *sonraiProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown value means it depends on another resource that has not been
	// applied yet. The provider cannot be configured from it, and saying so
	// beats failing later with a confusing authentication error.
	for name, value := range map[string]types.String{
		"org": config.Org, "domain": config.Domain, "token": config.Token,
	} {
		if value.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root(name),
				"Provider configuration is not known at plan time",
				"The "+name+" attribute depends on a value that is not known until apply. "+
					"Set it from a variable or environment variable instead.",
			)
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	cfg := client.Config{
		Org:       firstNonEmpty(config.Org.ValueString(), os.Getenv("SONRAI_ORG")),
		Domain:    firstNonEmpty(config.Domain.ValueString(), os.Getenv("SONRAI_DOMAIN")),
		Token:     firstNonEmpty(config.Token.ValueString(), os.Getenv("SONRAI_TOKEN")),
		UserAgent: "terraform-provider-sonrai/" + p.version,
	}

	if cfg.Org == "" {
		resp.Diagnostics.AddAttributeError(path.Root("org"), "Missing organization",
			"Set the org attribute or the SONRAI_ORG environment variable.")
	}
	if cfg.Token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing API token",
			"Set the SONRAI_TOKEN environment variable, or the token attribute.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	api, err := client.New(cfg)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	resp.ResourceData = api
	resp.DataSourceData = api
}

func (p *sonraiProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAccountDisabledServiceResource,
		NewAccountProtectedServiceResource,
		NewExemptedIdentityResource,
		NewJitConfigurationResource,
		NewRegionControlResource,
		NewControlSubmissionResource,
		NewControlResetResource,
	}
}

func (p *sonraiProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewScopeDataSource,
		NewSuggestedExemptionsDataSource,
		NewControlTransactionDataSource,
		NewDeployAccountDataSource,
		NewEntitlementsDataSource,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
