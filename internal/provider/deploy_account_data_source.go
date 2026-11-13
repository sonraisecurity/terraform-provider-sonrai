package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ datasource.DataSource              = (*deployAccountDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*deployAccountDataSource)(nil)
)

type deployAccountDataSource struct {
	api *client.Client
}

// NewDeployAccountDataSource returns the sonrai_deploy_account data source.
func NewDeployAccountDataSource() datasource.DataSource { return &deployAccountDataSource{} }

type deployAccountModel struct {
	ID                  types.String `tfsdk:"id"`
	RootScope           types.String `tfsdk:"root_scope"`
	AccountID           types.String `tfsdk:"account_id"`
	Delegated           types.Bool   `tfsdk:"delegated"`
	ManagementAccountID types.String `tfsdk:"management_account_id"`
	OrganizationID      types.String `tfsdk:"organization_id"`
	Onboarded           types.Bool   `tfsdk:"onboarded"`
}

func (d *deployAccountDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deploy_account"
}

func (d *deployAccountDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Resolves which cloud account a root scope's CloudFormation template must be " +
			"deployed into. This is the delegated admin account where one is configured, and the " +
			"management account otherwise.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `account_id`.",
				Computed:            true,
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
			},
			"account_id": schema.StringAttribute{
				MarkdownDescription: "Account to deploy the template into. Point AWS credentials at this.",
				Computed:            true,
			},
			"delegated": schema.BoolAttribute{
				MarkdownDescription: "True when `account_id` is a delegated admin account rather than the " +
					"management account.",
				Computed: true,
			},
			"management_account_id": schema.StringAttribute{
				MarkdownDescription: "The organization's management account. Equal to `account_id` only " +
					"when `delegated` is false.",
				Computed: true,
			},
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Cloud-native organization identifier, for example `o-abc123`.",
				Computed:            true,
			},
			"onboarded": schema.BoolAttribute{
				MarkdownDescription: "Whether the organization has completed onboarding.",
				Computed:            true,
			},
		},
	}
}

func (d *deployAccountDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *deployAccountDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config deployAccountModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	account, err := d.api.ResolveDeployAccount(ctx, config.RootScope.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to resolve deploy account", err)...)
		return
	}

	config.ID = types.StringValue(account.AccountID)
	config.AccountID = types.StringValue(account.AccountID)
	config.Delegated = types.BoolValue(account.Delegated)
	config.ManagementAccountID = types.StringValue(account.ManagementAccountID)
	config.OrganizationID = types.StringValue(account.OrganizationID)
	config.Onboarded = types.BoolValue(account.Onboarded)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
