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
	_ datasource.DataSource              = (*organizationPoliciesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*organizationPoliciesDataSource)(nil)
)

type organizationPoliciesDataSource struct {
	api *client.Client
}

// NewOrganizationPoliciesDataSource returns the sonrai_organization_policies data source.
func NewOrganizationPoliciesDataSource() datasource.DataSource {
	return &organizationPoliciesDataSource{}
}

type organizationPoliciesModel struct {
	ID        types.String `tfsdk:"id"`
	RootScope types.String `tfsdk:"root_scope"`
	Policies  types.List   `tfsdk:"policies"`
}

var organizationPolicyAttrTypes = map[string]attr.Type{
	"id":              types.StringType,
	"name":            types.StringType,
	"description":     types.StringType,
	"policy_type":     types.StringType,
	"content":         types.StringType,
	"authored":        types.BoolType,
	"managed":         types.BoolType,
	"attached_scopes": types.ListType{ElemType: types.StringType},
}

func (d *organizationPoliciesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_policies"
}

func (d *organizationPoliciesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the organization policies Sonrai knows of in an AWS organization: the " +
			"ones written in Sonrai, and the service control and resource control policies it " +
			"discovered in AWS.\n\n" +
			"Use it to see what exists before defining one with `sonrai_organization_policy`, which " +
			"rejects a name a managed policy already has, to find the id to import, and to see where a " +
			"policy is attached before destroying it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `root_scope`.",
				Computed:            true,
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Root scope of the AWS organization, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(1024),
					stringvalidator.RegexMatches(awsRootScopePattern,
						"must be an AWS root scope such as aws/r-abcd; organization policies are AWS only"),
				},
			},
			"policies": schema.ListNestedAttribute{
				MarkdownDescription: "Every policy, sorted by name.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "Sonrai's id for the policy. For a managed policy, this is " +
								"what `sonrai_organization_policy` imports and " +
								"`sonrai_organization_policy_attachment` takes as `policy_id`.",
							Computed: true,
						},
						"name":        schema.StringAttribute{Computed: true},
						"description": schema.StringAttribute{Computed: true},
						"policy_type": schema.StringAttribute{
							MarkdownDescription: "`" + client.ServiceControlPolicy + "` or `" + client.ResourceControlPolicy + "`.",
							Computed:            true,
						},
						"content": schema.StringAttribute{
							MarkdownDescription: "The policy document, as compact JSON.",
							Computed:            true,
						},
						"authored": schema.BoolAttribute{
							MarkdownDescription: "True for a policy written in Sonrai, false for one discovered " +
								"in AWS. Only authored policies can be imported into `sonrai_organization_policy`.",
							Computed: true,
						},
						"managed": schema.BoolAttribute{
							MarkdownDescription: "True when Sonrai deploys the policy. A discovered policy that " +
								"is not managed is only reported here; AWS still attaches it.",
							Computed: true,
						},
						"attached_scopes": schema.ListAttribute{
							MarkdownDescription: "Scope paths the policy is attached at: for a managed policy, " +
								"where Sonrai has deployed it; for an unmanaged one, where AWS attaches it. " +
								"An attachment that is staged and not yet deployed is not listed.",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *organizationPoliciesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *organizationPoliciesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config organizationPoliciesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rootScope := config.RootScope.ValueString()

	policies, err := d.api.OrganizationPolicies(ctx, rootScope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to list organization policies", err)...)
		return
	}

	values := make([]attr.Value, 0, len(policies))
	for _, policy := range policies {
		attached := policy.AttachedScopes
		if attached == nil {
			attached = []string{}
		}
		attachedScopes, diags := types.ListValueFrom(ctx, types.StringType, attached)
		resp.Diagnostics.Append(diags...)
		obj, diags := types.ObjectValue(organizationPolicyAttrTypes, map[string]attr.Value{
			"id":              types.StringValue(policy.ID),
			"name":            types.StringValue(policy.Name),
			"description":     types.StringValue(policy.Description),
			"policy_type":     types.StringValue(policy.Type),
			"content":         types.StringValue(policy.Content),
			"authored":        types.BoolValue(policy.Authored),
			"managed":         types.BoolValue(policy.Managed),
			"attached_scopes": attachedScopes,
		})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		values = append(values, obj)
	}

	list, diags := types.ListValue(types.ObjectType{AttrTypes: organizationPolicyAttrTypes}, values)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	config.ID = types.StringValue(rootScope)
	config.Policies = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
