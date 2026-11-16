package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ resource.Resource                 = (*jitConfigurationResource)(nil)
	_ resource.ResourceWithConfigure    = (*jitConfigurationResource)(nil)
	_ resource.ResourceWithImportState  = (*jitConfigurationResource)(nil)
	_ resource.ResourceWithUpgradeState = (*jitConfigurationResource)(nil)
)

type jitConfigurationResource struct {
	api *client.Client
}

// NewJitConfigurationResource returns the sonrai_jit_configuration resource.
func NewJitConfigurationResource() resource.Resource { return &jitConfigurationResource{} }

type jitConfigurationModel struct {
	ID           types.String `tfsdk:"id"`
	Scope        types.String `tfsdk:"scope"`
	DenyFirst    types.Bool   `tfsdk:"deny_first"`
	Entitlements types.Set    `tfsdk:"enrolled_entitlements"`
	Pending      types.Bool   `tfsdk:"pending"`
}

type jitEntitlementModel struct {
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	AutoPod types.Bool   `tfsdk:"auto_pod"`
}

func (r *jitConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_jit_configuration"
}

func (r *jitConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// Version 1 moved auto_pod from the scope onto each entitlement. See
		// UpgradeState.
		Version: 1,
		MarkdownDescription: "Configures Just-in-Time access at a scope: which entitlements are " +
			"enrolled, whether everything else is denied, and which entitlements have AutoPoD.\n\n" +
			"The platform replaces the whole configuration at a scope in one call, so this resource " +
			"is authoritative for the scope it names. An entitlement enrolled from the console and " +
			"left out of `enrolled_entitlements` is unenrolled on the next apply.\n\n" +
			"Enrolments inherited from an ancestor scope are reported by the platform but are not " +
			"managed here. Declare them on the ancestor's own resource.\n\n" +
			"Declaring this resource replaces whatever the scope already has: enrolments absent " +
			"from `enrolled_entitlements` are unenrolled, and `deny_first` takes the value " +
			"declared here.\n\n" +
			"AWS only for now.\n\n" +
			"Creating this resource *stages* the change. It takes effect only once the pending " +
			"changes for the containing root scope are submitted and deployed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The scope this configuration applies to.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Scope to configure: a root scope such as `aws/r-abcd`, or any " +
					"scope path beneath it. Enrolments apply to everything under the scope.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(awsScopePattern, "must be an aws/ scope; JIT configuration is AWS only for now"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"deny_first": schema.BoolAttribute{
				MarkdownDescription: "Deny every entitlement that is not enrolled at this scope. " +
					"Defaults to false.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"enrolled_entitlements": schema.SetNestedAttribute{
				MarkdownDescription: "Entitlements enrolled for JIT at this scope. Anything not " +
					"listed is not enrolled here. Omitting the attribute means \"enrol nothing\".",
				Optional: true,
				Computed: true,
				// Without a default, an omitted set plans as unknown and apply
				// never fills it, which the framework rejects.
				Default: setdefault.StaticValue(types.SetValueMust(
					types.ObjectType{AttrTypes: jitEntitlementAttrTypes}, nil)),
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "Name of the permission set, account access role " +
								"or SAML role. The platform identifies enrolments by name.",
							Required: true,
							Validators: []validator.String{
								stringvalidator.LengthAtLeast(1),
							},
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "`AWS_PERMISSION_SET` (an Identity Center permission " +
								"set), `AWS_ACCOUNT_ACCESS_ROLE` or `AWS_SAML_ROLE`. Defaults to " +
								"`AWS_PERMISSION_SET`.",
							Optional: true,
							Computed: true,
							Default:  stringdefault.StaticString(client.EntitlementPermissionSet),
							Validators: []validator.String{
								stringvalidator.OneOf(client.EntitlementTypes...),
							},
						},
						"auto_pod": schema.BoolAttribute{
							MarkdownDescription: "AutoPoD: a session through this entitlement is " +
								"approved for protected actions without a Permissions on Demand " +
								"request. Without it, protected actions still need one. Defaults to " +
								"false.",
							Optional: true,
							Computed: true,
							Default:  booldefault.StaticBool(false),
						},
					},
				},
			},
			"pending": schema.BoolAttribute{
				MarkdownDescription: "True while any part of this configuration has a staged change " +
					"that has not been submitted and deployed.",
				Computed: true,
			},
		},
	}
}

func (r *jitConfigurationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *jitConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan jitConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	staged, diags := r.apply(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !staged {
		// Nothing reached the platform, so there is nothing to record.
		return
	}
	// The configuration was staged, so state is saved even when the refresh
	// after it failed: erroring out without saving would leave a staged
	// configuration Terraform has no record of.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *jitConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state jitConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope := state.Scope.ValueString()
	cfg, err := r.api.JitConfiguration(ctx, scope)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "JIT configuration no longer present, removing from state",
				map[string]any{"scope": scope})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read JIT configuration", err)...)
		return
	}

	entitlements, diags := entitlementsToSet(ctx, cfg.Entitlements)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.ID = types.StringValue(scope)
	state.DenyFirst = types.BoolValue(cfg.DenyFirst)
	state.Entitlements = entitlements
	state.Pending = types.BoolValue(cfg.Pending)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *jitConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan jitConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The mutation replaces the whole configuration, so resending it in full
	// is the update.
	staged, diags := r.apply(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !staged {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *jitConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state jitConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope := state.Scope.ValueString()

	// Nothing defined at the scope -- already cleared, or the scope itself
	// gone from the hierarchy -- means there is nothing to stage, and writing
	// to a vanished scope would fail and strand the resource behind
	// `terraform state rm`.
	if _, err := r.api.JitConfiguration(ctx, scope); isNotFound(err) {
		return
	}

	// There is no delete for a JIT configuration: DeleteJitConfiguration takes
	// an id the read never returns, and the control-scoped deletes do not cover
	// JIT rows. Clearing it is a write of the empty configuration. Where the
	// change was staged and never deployed this cancels the staged row rather
	// than stacking a second one.
	if _, err := r.api.SetJitConfiguration(ctx, scope, false, nil); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to clear JIT configuration", err)...)
		return
	}

	resp.Diagnostics.AddWarning(
		"Clearing the JIT configuration is staged, not applied",
		fmt.Sprintf("Deny-first and every enrolment at %s have been staged for removal. They "+
			"remain in force until the pending changes for the root scope are submitted and "+
			"deployed, which a sonrai_control_submission covering that scope does later in this "+
			"same apply.", scope),
	)
}

func (r *jitConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !scopePattern.MatchString(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected a scope, for example %q, but got %q.", "aws/r-abcd", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("scope"), req.ID)...)
}

// apply stages the configuration and refreshes the computed fields on m.
// staged reports whether the write reached the platform, which is what the
// caller needs to decide between saving state and discarding it.
func (r *jitConfigurationResource) apply(ctx context.Context, m *jitConfigurationModel) (staged bool, diags diag.Diagnostics) {
	entitlements, d := entitlementsFromSet(ctx, m.Entitlements)
	diags.Append(d...)
	if diags.HasError() {
		return false, diags
	}

	scope := m.Scope.ValueString()
	denyFirst := m.DenyFirst.ValueBool()
	tflog.Info(ctx, "staging JIT configuration", map[string]any{
		"scope": scope, "deny_first": denyFirst, "entitlements": len(entitlements),
	})

	// Enrolling the account's broadest entitlement is legitimate -- it is the
	// posture where even administrators need a session -- but getting it wrong
	// locks everyone out until the change is reverted and redeployed.
	for _, e := range entitlements {
		if e.Name == "AdministratorAccess" {
			diags.AddWarning(
				"Enrolling AdministratorAccess in JIT",
				"Administrators will need an active JIT session at "+scope+" to use it. Confirm "+
					"another path into the account exists before deploying this.",
			)
		}
	}

	change, err := r.api.SetJitConfiguration(ctx, scope, denyFirst, entitlements)
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to set JIT configuration", err)...)
		return false, diags
	}
	tflog.Info(ctx, "staged JIT configuration", map[string]any{
		"scope": scope, "enrolled": change.Added, "unenrolled": change.Removed,
	})

	m.ID = types.StringValue(scope)
	// A staging call that just succeeded is by definition undeployed, so the
	// refresh below can only confirm it.
	m.Pending = types.BoolValue(true)

	cfg, err := r.api.JitConfiguration(ctx, scope)
	if err != nil {
		if !isNotFound(err) {
			diags.Append(diagnoseAPIError("Unable to read JIT configuration", err)...)
		}
		return true, diags
	}
	m.Pending = types.BoolValue(cfg.Pending)
	return true, diags
}

// jitConfigurationModelV0 is the state layout of schema version 0, which had a
// scope-level auto_pod and full_access on each entitlement.
type jitConfigurationModelV0 struct {
	ID           types.String `tfsdk:"id"`
	Scope        types.String `tfsdk:"scope"`
	DenyFirst    types.Bool   `tfsdk:"deny_first"`
	AutoPod      types.Bool   `tfsdk:"auto_pod"`
	Entitlements types.Set    `tfsdk:"enrolled_entitlements"`
	Pending      types.Bool   `tfsdk:"pending"`
}

type jitEntitlementModelV0 struct {
	Name       types.String `tfsdk:"name"`
	Type       types.String `tfsdk:"type"`
	FullAccess types.Bool   `tfsdk:"full_access"`
}

// UpgradeState carries version 0 state forward.
//
// full_access is what the console calls AutoPoD, so it becomes the
// entitlement's auto_pod. The scope-level auto_pod is dropped: the platform
// discards it for AWS scopes, so it never described anything deployed.
func (r *jitConfigurationResource) UpgradeState(_ context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema: &schema.Schema{
				Attributes: map[string]schema.Attribute{
					"id":         schema.StringAttribute{Computed: true},
					"scope":      schema.StringAttribute{Required: true},
					"deny_first": schema.BoolAttribute{Optional: true, Computed: true},
					"auto_pod":   schema.BoolAttribute{Optional: true, Computed: true},
					"enrolled_entitlements": schema.SetNestedAttribute{
						Optional: true,
						Computed: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name":        schema.StringAttribute{Required: true},
								"type":        schema.StringAttribute{Optional: true, Computed: true},
								"full_access": schema.BoolAttribute{Optional: true, Computed: true},
							},
						},
					},
					"pending": schema.BoolAttribute{Computed: true},
				},
			},
			StateUpgrader: upgradeJitConfigurationV0,
		},
	}
}

func upgradeJitConfigurationV0(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	var prior jitConfigurationModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}

	entitlements := []client.JitEntitlement{}
	if !prior.Entitlements.IsNull() && !prior.Entitlements.IsUnknown() {
		var models []jitEntitlementModelV0
		resp.Diagnostics.Append(prior.Entitlements.ElementsAs(ctx, &models, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		for _, m := range models {
			entitlements = append(entitlements, client.JitEntitlement{
				Name:       m.Name.ValueString(),
				Type:       m.Type.ValueString(),
				FullAccess: m.FullAccess.ValueBool(),
			})
		}
	}
	set, diags := entitlementsToSet(ctx, entitlements)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, jitConfigurationModel{
		ID:           prior.ID,
		Scope:        prior.Scope,
		DenyFirst:    prior.DenyFirst,
		Entitlements: set,
		Pending:      prior.Pending,
	})...)
}
