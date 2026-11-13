package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ resource.Resource                = (*accountProtectedServiceResource)(nil)
	_ resource.ResourceWithConfigure   = (*accountProtectedServiceResource)(nil)
	_ resource.ResourceWithImportState = (*accountProtectedServiceResource)(nil)
)

type accountProtectedServiceResource struct {
	api *client.Client
}

// NewAccountProtectedServiceResource returns the sonrai_account_protected_service resource.
func NewAccountProtectedServiceResource() resource.Resource {
	return &accountProtectedServiceResource{}
}

type accountProtectedServiceModel struct {
	ID                types.String `tfsdk:"id"`
	RootScope         types.String `tfsdk:"root_scope"`
	ResourceID        types.String `tfsdk:"resource_id"`
	ControlKey        types.String `tfsdk:"control_key"`
	ExemptIdentities  types.Set    `tfsdk:"exempt_identities"`
	ExemptSSOActorIDs types.Set    `tfsdk:"exempt_sso_actor_ids"`
	TimeboxedIdents   types.Set    `tfsdk:"timeboxed_exempt_identities"`
	TimeboxedSSO      types.Set    `tfsdk:"timeboxed_exempt_sso_actor_ids"`
	Scope             types.String `tfsdk:"scope"`
	Status            types.String `tfsdk:"status"`
	Pending           types.Bool   `tfsdk:"pending"`
}

func (r *accountProtectedServiceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_account_protected_service"
}

func (r *accountProtectedServiceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Protects one cloud service in one account, firewalling its sensitive " +
			"permissions for every identity except those exempted here.\n\n" +
			"A service already protected -- from the console, or by another configuration -- is " +
			"rejected rather than adopted.\n\n" +
			"Exemptions are attributes of the protect rather than resources of their own: each apply " +
			"converges the platform's exemption set on the configured one, and the platform removes " +
			"them when the control is unmanaged or switched to a disable.\n\n" +
			"The platform's own suggested exemptions are **not** applied. Read them with the " +
			"`sonrai_suggested_exemptions` data source and list the ones you want, so that the same " +
			"configuration always produces the same result.\n\n" +
			"Exemptions granted by a time-limited Permissions on Demand approval are reported in " +
			"`timeboxed_exempt_identities` and `timeboxed_exempt_sso_actor_ids` and are not managed " +
			"here: they expire on their own, and listing them would have Terraform fight the " +
			"approval workflow. A PoD approval granted **permanently** is indistinguishable from one " +
			"made here, so it is treated as drift and removed on the next apply unless it is added " +
			"to `exempt_identities`.\n\n" +
			"Creating this resource *stages* the change. It takes effect only once the pending changes " +
			"for the containing root scope are submitted and deployed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `control_key@root_scope@resource_id`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_id": schema.StringAttribute{
				MarkdownDescription: "Cloud-native id of the account to protect the service in: an AWS " +
					"account id, a GCP project id, or an Azure subscription id.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"control_key": schema.StringAttribute{
				MarkdownDescription: "Firewall control key for the service, for example `iam` or `lambda`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"exempt_identities": schema.SetAttribute{
				MarkdownDescription: "Cloud-native identities exempt from this control, as ARNs or " +
					"resource ids, for example `arn:aws:iam::123456789012:role/Admin`.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     setdefault.StaticValue(emptyStringSet()),
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"exempt_sso_actor_ids": schema.SetAttribute{
				MarkdownDescription: "SSO actor ids exempt from this control. Kept separate from " +
					"`exempt_identities` because the platform stores and matches them separately.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     setdefault.StaticValue(emptyStringSet()),
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"timeboxed_exempt_identities": schema.SetAttribute{
				MarkdownDescription: "Identities currently exempt from this control through a " +
					"time-limited Permissions on Demand grant. Reported for visibility; not managed.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"timeboxed_exempt_sso_actor_ids": schema.SetAttribute{
				MarkdownDescription: "SSO actor ids currently exempt from this control through a " +
					"time-limited Permissions on Demand grant. Reported for visibility; not managed.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path this resolved to, recomputed on every read.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Control status reported by the platform: `Protected`, `Disabled`, " +
					"`Unprotected` or `Non-Privileged`. Reads `Protected` from the moment a protect is staged.",
				Computed: true,
			},
			"pending": schema.BoolAttribute{
				MarkdownDescription: "True while this control has a staged change that has not been submitted and deployed.",
				Computed:            true,
			},
		},
	}
}

func (r *accountProtectedServiceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *accountProtectedServiceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan accountProtectedServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A service someone already protected is rejected rather than adopted:
	// recording it would have destroy unmanage a control this configuration
	// never caused, discarding exemptions along with it. Update is different
	// -- re-applying while Protected is how exemptions change.
	scope, err := r.resolveScope(ctx, &plan)
	if err != nil {
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}
	controlKey := plan.ControlKey.ValueString()
	if status, err := r.api.ServiceControlStatus(ctx, controlKey, scope); err == nil &&
		status.Status == client.StatusProtected {
		resp.Diagnostics.AddAttributeError(
			path.Root("control_key"),
			"Service is already protected",
			fmt.Sprintf("%s at %s is already protected. Import the existing control instead: "+
				"terraform import <address> '%s'", controlKey, scope, protectedServiceID(plan)),
		)
		return
	}

	staged, diags := r.apply(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !staged {
		// Nothing reached the platform, so there is nothing to record.
		return
	}
	// The protect was staged. Saving state even when refreshing it afterwards
	// failed is what stops a staged control existing that Terraform has no
	// record of, and cannot later remove.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountProtectedServiceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state accountProtectedServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, err := r.resolveScope(ctx, &state)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	controlKey := state.ControlKey.ValueString()
	status, err := r.api.ServiceControlStatus(ctx, controlKey, scope)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read service control status", err)...)
		return
	}

	// Anything other than Protected means this control is gone -- unmanaged, or
	// replaced by a disable, which also discards its exceptions.
	if status.Status != client.StatusProtected {
		tflog.Info(ctx, "service protect no longer present, removing from state", map[string]any{
			"control_key": controlKey, "scope": scope, "status": status.Status,
		})
		resp.State.RemoveResource(ctx)
		return
	}

	exceptions, err := r.api.ControlExceptions(ctx, controlKey, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read control exceptions", err)...)
		return
	}

	timeboxed, diags := r.timeboxed(ctx, scope, exceptions.ControlID)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	identities, diags := stringsToSet(ctx, difference(exceptions.Identities, timeboxed.Identities))
	resp.Diagnostics.Append(diags...)
	ssoActors, diags := stringsToSet(ctx, difference(exceptions.SSOActorIDs, timeboxed.SSOActorIDs))
	resp.Diagnostics.Append(diags...)
	timeboxedIdents, diags := stringsToSet(ctx, timeboxed.Identities)
	resp.Diagnostics.Append(diags...)
	timeboxedSSO, diags := stringsToSet(ctx, timeboxed.SSOActorIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.ExemptIdentities = identities
	state.ExemptSSOActorIDs = ssoActors
	state.TimeboxedIdents = timeboxedIdents
	state.TimeboxedSSO = timeboxedSSO
	state.Status = types.StringValue(status.Status)
	state.Pending = types.BoolValue(status.Pending)
	state.ID = types.StringValue(protectedServiceID(state))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *accountProtectedServiceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan accountProtectedServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only the exemption sets can reach here; the rest force replacement.
	// apply stages whatever is newly configured and explicitly removes what was
	// dropped, so resending the full sets is the update.
	staged, diags := r.apply(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !staged {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountProtectedServiceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state accountProtectedServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, err := r.resolveScope(ctx, &state)
	if err != nil {
		// An account that left the hierarchy took its controls with it; failing
		// here would leave a resource only `terraform state rm` can clear.
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	controlKey := state.ControlKey.ValueString()
	applied, _ := wasApplied(ctx, r.api, scope, controlKey)

	if err := r.api.UnmanageService(ctx, controlKey, scope); err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to unprotect service", err)...)
		return
	}

	if applied {
		resp.Diagnostics.AddWarning(
			"Unprotecting the service is staged, not applied",
			"The protect for "+controlKey+" at "+scope+" has been staged for removal, along with its "+
				"exemptions. It remains in force until the pending changes for its root scope are "+
				"submitted and deployed, which a sonrai_control_submission covering that scope does "+
				"later in this same apply.",
		)
	}
}

func (r *accountProtectedServiceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importControlID(ctx, req, resp, "iam"+importSeparator+"aws/r-abcd"+importSeparator+"123456789012")
}

// apply resolves the scope, stages the protect with the declared exemptions,
// and refreshes the computed fields on m.
//
// staged reports whether the protect reached the platform, which is what the
// caller needs to decide between saving state and discarding it.
func (r *accountProtectedServiceResource) apply(ctx context.Context, m *accountProtectedServiceModel) (staged bool, diags diag.Diagnostics) {
	scope, err := r.resolveScope(ctx, m)
	if err != nil {
		diags.Append(diagnoseResolveError(err)...)
		return false, diags
	}

	identities, d := setToStrings(ctx, m.ExemptIdentities)
	diags.Append(d...)
	ssoActors, d := setToStrings(ctx, m.ExemptSSOActorIDs)
	diags.Append(d...)
	if diags.HasError() {
		return false, diags
	}

	controlKey := m.ControlKey.ValueString()
	tflog.Info(ctx, "staging service protect", map[string]any{
		"control_key": controlKey, "scope": scope,
		"exempt_identities": len(identities), "exempt_sso_actor_ids": len(ssoActors),
	})

	issues, err := r.api.ProtectService(ctx, controlKey, scope, identities, ssoActors)
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to protect service", err)...)
		return false, diags
	}
	m.ID = types.StringValue(protectedServiceID(*m))
	// Issues without failures are advisory -- accounts the protect could not
	// cover yet, for instance -- and would otherwise be invisible.
	if issues != nil && issues.TotalCount > 0 {
		diags.AddWarning(
			fmt.Sprintf("Protecting %s reported %d issue(s)", controlKey, issues.TotalCount),
			strings.Join(issues.Messages(), "\n"),
		)
	}

	status, err := r.api.ServiceControlStatus(ctx, controlKey, scope)
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to read service control status", err)...)
		return true, diags
	}

	m.Status = types.StringValue(status.Status)
	m.Pending = types.BoolValue(status.Pending)

	// Computed attributes have to be known once the apply returns, so these
	// are read now rather than left for the next refresh. The control id comes
	// from the exception rows: CloudServiceUsage does not carry one, and a
	// timeboxed grant always has an exception to be found on.
	exceptions, err := r.api.ControlExceptions(ctx, controlKey, scope)
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to read control exceptions", err)...)
		return true, diags
	}
	timeboxed, d := r.timeboxed(ctx, scope, exceptions.ControlID)
	diags.Append(d...)
	if diags.HasError() {
		return true, diags
	}

	// ProtectService is add-only on the platform, so identities dropped from
	// configuration must be removed explicitly. Timeboxed PoD grants are
	// excluded: they are ordinary applied exceptions with no origin marker, and
	// removing them here would revoke a live approval.
	rootScope := m.RootScope.ValueString()
	for _, ident := range difference(difference(exceptions.Identities, identities), timeboxed.Identities) {
		if err := r.api.RemoveControlException(ctx, rootScope, scope, exceptions.ControlID, controlKey, ident, false); err != nil {
			diags.Append(diagnoseAPIError("Unable to remove exemption for "+ident, err)...)
			return true, diags
		}
	}
	for _, actor := range difference(difference(exceptions.SSOActorIDs, ssoActors), timeboxed.SSOActorIDs) {
		if err := r.api.RemoveControlException(ctx, rootScope, scope, exceptions.ControlID, controlKey, actor, true); err != nil {
			diags.Append(diagnoseAPIError("Unable to remove exemption for "+actor, err)...)
			return true, diags
		}
	}

	m.TimeboxedIdents, d = stringsToSet(ctx, timeboxed.Identities)
	diags.Append(d...)
	m.TimeboxedSSO, d = stringsToSet(ctx, timeboxed.SSOActorIDs)
	diags.Append(d...)
	if timeboxed.Has() {
		diags.AddWarning(
			fmt.Sprintf("%s has exemptions granted on demand", controlKey),
			"These expire on their own and are not managed here:\n"+
				strings.Join(append(timeboxed.Identities, timeboxed.SSOActorIDs...), "\n"),
		)
	}
	return true, diags
}

// timeboxed reads the on-demand grants for one control, tolerating a control
// whose id could not be determined.
func (r *accountProtectedServiceResource) timeboxed(ctx context.Context, scope, id string) (*client.TimeboxedExemptions, diag.Diagnostics) {
	var diags diag.Diagnostics
	if id == "" {
		return &client.TimeboxedExemptions{}, diags
	}
	timeboxed, err := r.api.TimeboxedExemptions(ctx, scope, id)
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to read on-demand grants", err)...)
		return &client.TimeboxedExemptions{}, diags
	}
	return timeboxed, diags
}

func protectedServiceID(m accountProtectedServiceModel) string {
	return controlID(m.ControlKey, m.RootScope, m.ResourceID)
}
