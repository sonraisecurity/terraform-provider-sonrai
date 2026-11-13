package provider

import (
	"context"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ datasource.DataSource              = (*entitlementsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*entitlementsDataSource)(nil)
)

type entitlementsDataSource struct {
	api *client.Client
}

// NewEntitlementsDataSource returns the sonrai_entitlements data source.
func NewEntitlementsDataSource() datasource.DataSource { return &entitlementsDataSource{} }

type entitlementModel struct {
	Name             types.String `tfsdk:"name"`
	Type             types.String `tfsdk:"type"`
	Labels           types.Set    `tfsdk:"labels"`
	UserCount        types.Int64  `tfsdk:"user_count"`
	UserCountDisplay types.String `tfsdk:"user_count_display"`
}

type entitlementsModel struct {
	ID           types.String       `tfsdk:"id"`
	Scope        types.String       `tfsdk:"scope"`
	Type         types.String       `tfsdk:"type"`
	Names        types.Set          `tfsdk:"names"`
	Entitlements []entitlementModel `tfsdk:"entitlements"`
}

func (d *entitlementsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entitlements"
}

func (d *entitlementsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "AWS entitlements available at a scope: Identity Center permission " +
			"sets, account access roles and SAML roles.\n\n" +
			"`sonrai_jit_configuration` enrols entitlements by name, and a name that matches nothing " +
			"enrols nothing without reporting an error. Read them here rather than copying them out " +
			"of the console.\n\n" +
			"`user_count` is how many identities reach the scope through the entitlement today, which " +
			"is usually what decides whether enrolling it is disruptive. It is null where no count " +
			"applies, as it does not for a SAML role assigned in the identity provider; read " +
			"`user_count_display` to render it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The scope these entitlements were read at.",
				Computed:            true,
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Scope to read entitlements at, for example " +
					"`aws/r-abcd/123456789012`.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(awsScopePattern, "must be an aws/ scope; entitlements are AWS only for now"),
				},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Return only entitlements of this type: `AWS_PERMISSION_SET`, " +
					"`AWS_ACCOUNT_ACCESS_ROLE` or `AWS_SAML_ROLE`. Omit for all three. An identity " +
					"that qualifies as more than one type appears only under the more specific one, " +
					"account access role ahead of SAML role, so the filtered lists never overlap.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.EntitlementTypes...),
				},
			},
			"names": schema.SetAttribute{
				MarkdownDescription: "Just the names, for passing to `enrolled_entitlements`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"entitlements": schema.ListNestedAttribute{
				MarkdownDescription: "Each entitlement with its type and current usage, by name.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Computed: true},
						"type": schema.StringAttribute{Computed: true},
						"labels": schema.SetAttribute{
							ElementType: types.StringType,
							Computed:    true,
						},
						"user_count": schema.Int64Attribute{
							MarkdownDescription: "Identities with access through this entitlement. " +
								"Null where no count applies, which is not the same as zero.",
							Computed: true,
						},
						"user_count_display": schema.StringAttribute{
							MarkdownDescription: "`user_count` rendered for display, or `N/A` where " +
								"no count applies.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *entitlementsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *entitlementsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config entitlementsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope := config.Scope.ValueString()
	entitlements, err := d.api.Entitlements(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read entitlements", err)...)
		return
	}

	wanted := config.Type.ValueString()

	// Start empty rather than nil so a scope with nothing to report reads as an
	// empty set instead of null.
	names := []string{}
	config.Entitlements = make([]entitlementModel, 0, len(entitlements))
	for _, e := range entitlements {
		if wanted != "" && e.Type != wanted {
			continue
		}
		labels, diags := stringsToSet(ctx, e.Labels)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		names = append(names, e.Name)
		// Absent means no access count applies, as it does not for a SAML role
		// assigned in the identity provider. Reporting that as zero would read
		// as "nobody uses this". The display form is derived here rather than
		// queried, so the provider keeps working against servers that predate
		// the userCountDisplay field.
		userCount := types.Int64Null()
		display := "N/A"
		if e.UserCount != nil {
			userCount = types.Int64Value(*e.UserCount)
			display = strconv.FormatInt(*e.UserCount, 10)
		}
		config.Entitlements = append(config.Entitlements, entitlementModel{
			Name:             types.StringValue(e.Name),
			Type:             types.StringValue(e.Type),
			Labels:           labels,
			UserCount:        userCount,
			UserCountDisplay: types.StringValue(display),
		})
	}

	nameSet, diags := stringsToSet(ctx, names)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.ID = types.StringValue(scope)
	config.Names = nameSet
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
