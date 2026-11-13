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
	_ datasource.DataSource              = (*suggestedExemptionsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*suggestedExemptionsDataSource)(nil)
)

type suggestedExemptionsDataSource struct {
	api *client.Client
}

// NewSuggestedExemptionsDataSource returns the sonrai_suggested_exemptions data source.
func NewSuggestedExemptionsDataSource() datasource.DataSource {
	return &suggestedExemptionsDataSource{}
}

type suggestedExemptionModel struct {
	ResourceID   types.String `tfsdk:"resource_id"`
	SSOActorID   types.String `tfsdk:"sso_actor_id"`
	Name         types.String `tfsdk:"name"`
	IsSSO        types.Bool   `tfsdk:"is_sso"`
	LastUsed     types.String `tfsdk:"last_sensitive_used"`
	LastUsedDate types.String `tfsdk:"last_sensitive_used_date"`
}

type suggestedExemptionsModel struct {
	ID          types.String              `tfsdk:"id"`
	RootScope   types.String              `tfsdk:"root_scope"`
	ResourceID  types.String              `tfsdk:"resource_id"`
	ControlKey  types.String              `tfsdk:"control_key"`
	Scope       types.String              `tfsdk:"scope"`
	Identities  types.Set                 `tfsdk:"identities"`
	SSOActorIDs types.Set                 `tfsdk:"sso_actor_ids"`
	Suggestions []suggestedExemptionModel `tfsdk:"suggestions"`
}

func (d *suggestedExemptionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_suggested_exemptions"
}

func (d *suggestedExemptionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Identities the platform would exempt when protecting a service, because " +
			"they recently used one of its sensitive permissions.\n\n" +
			"`sonrai_account_protected_service` does not apply these automatically. Review them, then " +
			"list the ones you want in `exempt_identities` and `exempt_sso_actor_ids`.\n\n" +
			"The lookback window is an organization setting, so this set changes over time on its own. " +
			"Referencing `identities` directly keeps exemptions tracking the window; copying the values " +
			"into configuration keeps them stable until you change them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `control_key@scope`.",
				Computed:            true,
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
			},
			"resource_id": schema.StringAttribute{
				MarkdownDescription: "Cloud-native id of the account.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"control_key": schema.StringAttribute{
				MarkdownDescription: "Firewall control key for the service, for example `iam`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path the account resolved to.",
				Computed:            true,
			},
			"identities": schema.SetAttribute{
				MarkdownDescription: "Suggested non-SSO identities, ready to pass to `exempt_identities`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"sso_actor_ids": schema.SetAttribute{
				MarkdownDescription: "Suggested SSO actor ids, ready to pass to `exempt_sso_actor_ids`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"suggestions": schema.ListNestedAttribute{
				MarkdownDescription: "Each suggestion with the usage that produced it.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"resource_id":              schema.StringAttribute{Computed: true},
						"sso_actor_id":             schema.StringAttribute{Computed: true},
						"name":                     schema.StringAttribute{Computed: true},
						"is_sso":                   schema.BoolAttribute{Computed: true},
						"last_sensitive_used":      schema.StringAttribute{Computed: true},
						"last_sensitive_used_date": schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *suggestedExemptionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *suggestedExemptionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config suggestedExemptionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, err := resolveAccountScope(ctx, d.api,
		config.RootScope.ValueString(), config.ResourceID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	controlKey := config.ControlKey.ValueString()
	suggestions, err := d.api.SuggestedExceptions(ctx, controlKey, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read suggested exemptions", err)...)
		return
	}

	// Start empty rather than nil so an absent category reads as an empty set
	// instead of null, which would be awkward to use in configuration.
	identities := []string{}
	ssoActorIDs := []string{}
	config.Suggestions = make([]suggestedExemptionModel, 0, len(suggestions))
	for _, s := range suggestions {
		if s.IsSSO {
			if s.SSOActorID != "" {
				ssoActorIDs = append(ssoActorIDs, s.SSOActorID)
			}
		} else if s.ResourceID != "" {
			identities = append(identities, s.ResourceID)
		}
		config.Suggestions = append(config.Suggestions, suggestedExemptionModel{
			ResourceID:   types.StringValue(s.ResourceID),
			SSOActorID:   types.StringValue(s.SSOActorID),
			Name:         types.StringValue(s.Name),
			IsSSO:        types.BoolValue(s.IsSSO),
			LastUsed:     types.StringValue(s.LastUsed),
			LastUsedDate: types.StringValue(s.LastUsedDate),
		})
	}

	identitySet, diags := stringsToSet(ctx, identities)
	resp.Diagnostics.Append(diags...)
	ssoSet, diags := stringsToSet(ctx, ssoActorIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	config.Scope = types.StringValue(scope)
	config.Identities = identitySet
	config.SSOActorIDs = ssoSet
	config.ID = types.StringValue(controlKey + importSeparator + scope)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
