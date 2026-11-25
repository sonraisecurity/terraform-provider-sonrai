package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ datasource.DataSource              = (*pendingChangesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*pendingChangesDataSource)(nil)
)

type pendingChangesDataSource struct {
	api *client.Client
}

// NewPendingChangesDataSource returns the sonrai_pending_changes data source.
func NewPendingChangesDataSource() datasource.DataSource { return &pendingChangesDataSource{} }

type pendingChangesModel struct {
	ID        types.String `tfsdk:"id"`
	RootScope types.String `tfsdk:"root_scope"`
	Count     types.Int64  `tfsdk:"total"`
	Changes   types.List   `tfsdk:"changes"`
}

var pendingChangeAttrTypes = map[string]attr.Type{
	"id":                types.StringType,
	"change_type":       types.StringType,
	"name":              types.StringType,
	"action":            types.StringType,
	"scope":             types.StringType,
	"control_key":       types.StringType,
	"staged_by":         types.StringType,
	"identities":        types.ListType{ElemType: types.StringType},
	"organization_wide": types.BoolType,
}

func (d *pendingChangesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pending_changes"
}

func (d *pendingChangesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the changes a `sonrai_control_submission` under a root scope would " +
			"deploy if it ran now with this token: those the token's identity has staged and not yet " +
			"submitted, and those the platform has queued for the whole organization. Changes another " +
			"user staged are not listed, because that user's submission deploys them, not this one.\n\n" +
			"It is read while planning, before this configuration stages anything, so what it lists " +
			"came from somewhere else: an earlier apply that did not finish, another configuration or " +
			"a console session using the same identity, or the platform. Do not give it a `depends_on` " +
			"on the controls. That defers the read until they have staged, and it then lists them too.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `root_scope`.",
				Computed:            true,
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
			},
			"total": schema.Int64Attribute{
				MarkdownDescription: "Number of pending changes.",
				Computed:            true,
			},
			"changes": schema.ListNestedAttribute{
				MarkdownDescription: "The pending changes.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Identifier of the change, unique within its `change_type`.",
							Computed:            true,
						},
						"change_type": schema.StringAttribute{
							MarkdownDescription: "Kind of change as the platform reports it, for example " +
								"`mapping`, `exception`, `exemption`, `region`, `third_party_access` or " +
								"`third_party_account`.",
							Computed: true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "Description of the change, as Pending Changes shows it in the console.",
							Computed:            true,
						},
						"action": schema.StringAttribute{
							MarkdownDescription: "What the change does, for example `ADD` or `REMOVE`.",
							Computed:            true,
						},
						"scope": schema.StringAttribute{
							MarkdownDescription: "Scope the change applies to.",
							Computed:            true,
						},
						"control_key": schema.StringAttribute{
							MarkdownDescription: "Control, third party or policy the change concerns, where it has one.",
							Computed:            true,
						},
						"staged_by": schema.StringAttribute{
							MarkdownDescription: "Identity the platform reports the change under. Changes " +
								"queued by the platform are reported under the caller, so use " +
								"`organization_wide` to tell them apart.",
							Computed: true,
						},
						"identities": schema.ListAttribute{
							MarkdownDescription: "Identities or accounts the change adds or removes, where it has any.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"organization_wide": schema.BoolAttribute{
							MarkdownDescription: "True when the platform queued the change for every " +
								"submitter rather than this identity staging it: updates to known third " +
								"party accounts, and edits to custom controls and organization policies " +
								"that are already deployed.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *pendingChangesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *pendingChangesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config pendingChangesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rootScope := config.RootScope.ValueString()
	changes, err := d.api.PendingChanges(ctx, rootScope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to list pending changes", err)...)
		return
	}

	objType := types.ObjectType{AttrTypes: pendingChangeAttrTypes}
	values := make([]attr.Value, 0, len(changes))
	for _, change := range changes {
		identities, diags := types.ListValueFrom(ctx, types.StringType, append([]string{}, change.Identities...))
		resp.Diagnostics.Append(diags...)
		obj, diags := types.ObjectValue(pendingChangeAttrTypes, map[string]attr.Value{
			"id":                types.StringValue(change.ID),
			"change_type":       types.StringValue(change.ChangeType),
			"name":              types.StringValue(change.Name),
			"action":            types.StringValue(change.Action),
			"scope":             types.StringValue(change.Scope),
			"control_key":       types.StringValue(change.ControlKey),
			"staged_by":         types.StringValue(change.StagedBy),
			"identities":        identities,
			"organization_wide": types.BoolValue(change.OrganizationWide()),
		})
		resp.Diagnostics.Append(diags...)
		values = append(values, obj)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	list, diags := types.ListValue(objType, values)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.ID = types.StringValue(rootScope)
	config.Count = types.Int64Value(int64(len(changes)))
	config.Changes = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
