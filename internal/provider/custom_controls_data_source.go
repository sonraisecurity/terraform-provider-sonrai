package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ datasource.DataSource              = (*customControlsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*customControlsDataSource)(nil)
)

// The clouds custom controls exist for.
var customControlsRootPattern = regexp.MustCompile(`^(aws|gcp)/[^/\s]+$`)

type customControlsDataSource struct {
	api *client.Client
}

// NewCustomControlsDataSource returns the sonrai_custom_controls data source.
func NewCustomControlsDataSource() datasource.DataSource { return &customControlsDataSource{} }

type customControlsModel struct {
	ID          types.String `tfsdk:"id"`
	RootScope   types.String `tfsdk:"root_scope"`
	ControlKeys types.Set    `tfsdk:"control_keys"`
	Controls    types.List   `tfsdk:"controls"`
}

var customControlAttrTypes = map[string]attr.Type{
	"control_key":           types.StringType,
	"title":                 types.StringType,
	"description":           types.StringType,
	"cloud_type":            types.StringType,
	"permissions":           types.SetType{ElemType: types.StringType},
	"deployed_scopes":       types.ListType{ElemType: types.StringType},
	"deployed_resource_ids": types.ListType{ElemType: types.StringType},
}

func (d *customControlsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_controls"
}

func (d *customControlsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the custom controls defined in the organization, with the " +
			"permissions each covers and where it is deployed under a root scope.\n\n" +
			"Custom controls belong to the whole organization, so the list includes ones made in the " +
			"console and by other configurations. Use it to see what exists before defining one with " +
			"`sonrai_custom_control`, which rejects a name that is already taken, and to find where a " +
			"control is still deployed before destroying it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `root_scope`.",
				Computed:            true,
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope, for example `aws/r-abcd`. It " +
					"selects the cloud whose controls are listed, and `deployed_scopes` reports " +
					"deployments under it.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(1024),
					stringvalidator.RegexMatches(customControlsRootPattern,
						"must be an aws/ or gcp/ root scope; custom controls are not available elsewhere"),
				},
			},
			"control_keys": schema.SetAttribute{
				MarkdownDescription: "Just the control keys.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"controls": schema.ListNestedAttribute{
				MarkdownDescription: "Every custom control, sorted by control key.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"control_key": schema.StringAttribute{
							MarkdownDescription: "Key to give `sonrai_account_protected_service` or " +
								"`sonrai_account_disabled_service` to attach the control.",
							Computed: true,
						},
						"title":       schema.StringAttribute{Computed: true},
						"description": schema.StringAttribute{Computed: true},
						"cloud_type":  schema.StringAttribute{Computed: true},
						"permissions": schema.SetAttribute{
							MarkdownDescription: "Permissions the control covers, as the platform spells them.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"deployed_scopes": schema.ListAttribute{
							MarkdownDescription: "Scope paths under `root_scope` where the control is " +
								"deployed. A control staged at a scope and not yet deployed is not " +
								"listed. Empty means the control can be destroyed as far as this root " +
								"scope is concerned.",
							ElementType: types.StringType,
							Computed:    true,
						},
						"deployed_resource_ids": schema.ListAttribute{
							MarkdownDescription: "Cloud-native id of each entry in `deployed_scopes`, in " +
								"the same order: what resources take as `resource_id`.",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *customControlsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *customControlsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config customControlsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rootScope := config.RootScope.ValueString()

	controls, err := d.api.CustomControls(ctx, rootScope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to list custom controls", err)...)
		return
	}

	objType := types.ObjectType{AttrTypes: customControlAttrTypes}
	values := make([]attr.Value, 0, len(controls))
	controlKeys := make([]string, 0, len(controls))
	for _, control := range controls {
		deployments, err := d.api.CustomControlDeployments(ctx, control.CloudType, control.Key)
		if err != nil {
			resp.Diagnostics.Append(diagnoseAPIError("Unable to read where a custom control is deployed", err)...)
			return
		}
		deployed := []string{}
		for _, dep := range deployments {
			if dep.RootScope == rootScope {
				deployed = append(deployed, dep.Scope)
			}
		}

		permissions, diags := stringsToSet(ctx, control.Permissions)
		resp.Diagnostics.Append(diags...)
		deployedScopes, diags := types.ListValueFrom(ctx, types.StringType, deployed)
		resp.Diagnostics.Append(diags...)
		deployedIDs, diags := types.ListValueFrom(ctx, types.StringType, scopeResourceIDs(deployed))
		resp.Diagnostics.Append(diags...)
		obj, diags := types.ObjectValue(customControlAttrTypes, map[string]attr.Value{
			"control_key":           types.StringValue(control.Key),
			"title":                 types.StringValue(control.Title),
			"description":           types.StringValue(control.Description),
			"cloud_type":            types.StringValue(control.CloudType),
			"permissions":           permissions,
			"deployed_scopes":       deployedScopes,
			"deployed_resource_ids": deployedIDs,
		})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		values = append(values, obj)
		controlKeys = append(controlKeys, control.Key)
	}

	list, diags := types.ListValue(objType, values)
	resp.Diagnostics.Append(diags...)
	keySet, diags := stringsToSet(ctx, controlKeys)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.ID = types.StringValue(rootScope)
	config.ControlKeys = keySet
	config.Controls = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
