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
	_ datasource.DataSource              = (*scopeDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*scopeDataSource)(nil)
)

type scopeDataSource struct {
	api *client.Client
}

// NewScopeDataSource returns the sonrai_scope data source.
func NewScopeDataSource() datasource.DataSource { return &scopeDataSource{} }

type scopeModel struct {
	ID          types.String `tfsdk:"id"`
	RootScope   types.String `tfsdk:"root_scope"`
	ResourceID  types.String `tfsdk:"resource_id"`
	EntryType   types.String `tfsdk:"entry_type"`
	Scope       types.String `tfsdk:"scope"`
	Name        types.String `tfsdk:"name"`
	CloudType   types.String `tfsdk:"cloud_type"`
	ParentScope types.String `tfsdk:"parent_scope"`
}

func (d *scopeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scope"
}

func (d *scopeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up the canonical scope path for anything in the cloud hierarchy: an " +
			"account, GCP project, Azure subscription, OU, folder, management group, or the root itself.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `scope`.",
				Computed:            true,
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope to search under, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
			},
			"resource_id": schema.StringAttribute{
				MarkdownDescription: "Cloud-native identifier of the thing to resolve: an AWS account id or " +
					"`ou-` id, a GCP project or folder id, an Azure subscription or management group id, or " +
					"the root's own id such as `r-abcd`. Ids may be given bare — `my-project` resolves the " +
					"same as `projects|my-project`.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"entry_type": schema.StringAttribute{
				MarkdownDescription: "Hierarchy entry type to match, such as `account`, `organizationalUnit`, " +
					"`project`, `folder`, `subscription` or `managementGroup`. Only needed when an id could " +
					"name more than one kind of thing; otherwise the type that matched is reported back.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path. Pass this to a control resource's `scope`.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Display name of the account, project or subscription.",
				Computed:            true,
			},
			"cloud_type": schema.StringAttribute{
				MarkdownDescription: "Cloud this entry belongs to.",
				Computed:            true,
			},
			"parent_scope": schema.StringAttribute{
				MarkdownDescription: "Scope of the OU, folder or management group directly above this entry.",
				Computed:            true,
			},
		},
	}
}

func (d *scopeDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *scopeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config scopeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	entry, err := d.api.ResolveScope(ctx,
		config.RootScope.ValueString(),
		config.ResourceID.ValueString(),
		config.EntryType.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to resolve scope", err)...)
		return
	}

	config.ID = types.StringValue(entry.Scope)
	config.EntryType = types.StringValue(entry.EntryType)
	config.Scope = types.StringValue(entry.Scope)
	config.Name = types.StringValue(entry.Name)
	config.CloudType = types.StringValue(entry.CloudType)
	config.ParentScope = types.StringValue(entry.ParentScope)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
