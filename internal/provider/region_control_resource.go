package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

// Configuration spellings for the two states a region control can declare.
const (
	regionStateEnabled  = "enabled"
	regionStateDisabled = "disabled"
)

// awsRegionPattern covers AWS region identifiers, including partitioned ones
// such as us-gov-west-1. The resource is AWS-only for now, matching the scope
// validator below.
var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)

var awsScopePattern = regexp.MustCompile(`^aws/`)

var (
	_ resource.Resource                = (*regionControlResource)(nil)
	_ resource.ResourceWithConfigure   = (*regionControlResource)(nil)
	_ resource.ResourceWithImportState = (*regionControlResource)(nil)
)

type regionControlResource struct {
	api *client.Client
}

// NewRegionControlResource returns the sonrai_region_control resource.
func NewRegionControlResource() resource.Resource { return &regionControlResource{} }

type regionControlModel struct {
	ID            types.String `tfsdk:"id"`
	RootScope     types.String `tfsdk:"root_scope"`
	ResourceID    types.String `tfsdk:"resource_id"`
	RegionID      types.String `tfsdk:"region_id"`
	State         types.String `tfsdk:"state"`
	PreviousState types.String `tfsdk:"previous_state"`
	Scope         types.String `tfsdk:"scope"`
	Status        types.String `tfsdk:"status"`
	Pending       types.Bool   `tfsdk:"pending"`
	Critical      types.Bool   `tfsdk:"critical"`
	RegionName    types.String `tfsdk:"region_name"`
}

func (r *regionControlResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_region_control"
}

func (r *regionControlResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enables or disables one cloud region in one account.\n\n" +
			"Regions are enabled by default, and which regions exist comes from the cloud: the " +
			"platform tracks what the account has switched on, so a region the account does not " +
			"have cannot be controlled.\n\n" +
			"Accounts only, like the service controls: staging a region control at an OU or root " +
			"expands to the accounts present at the time and does not cover accounts added later, " +
			"while the container's aggregate status hides the gap. Declaring per account -- " +
			"typically with `for_each` over the account list -- is what makes a new account show " +
			"up as a planned change instead of silently uncovered.\n\n" +
			"`state` is declared rather than implied, because enabling is an override and not " +
			"merely the absence of a disable: one account can opt back in to a region disabled " +
			"above it.\n\n" +
			"A declaration that would change nothing is rejected rather than adopted: declaring " +
			"`enabled` where nothing disables the region, or `disabled` where it already is, " +
			"records a control this configuration never caused, and destroying it later would " +
			"mutate state this configuration never owned. Import an existing control instead.\n\n" +
			"Destroying the resource restores the state the account had before it: a disable is " +
			"lifted, an override is withdrawn.\n\n" +
			"Disabling a region stops the firewall deploying into it, so an account with controls " +
			"gets no EventBridge rule there.\n\n" +
			"AWS only for now.\n\n" +
			"Creating this resource *stages* the change. It takes effect only once the pending " +
			"changes for the containing root scope are submitted and deployed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `region_id@root_scope@resource_id`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope, for example `aws/r-abcd`. " +
					"AWS only for now.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(awsScopePattern, "must be an aws/ scope; region controls are AWS only for now"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_id": schema.StringAttribute{
				MarkdownDescription: "Cloud-native id of the account to control the region in: an " +
					"AWS account id. Organizational units and roots are not accepted; declare the " +
					"member accounts individually so accounts added later surface as planned changes.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Cloud region identifier, for example `ap-south-1`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(awsRegionPattern, "must be an AWS region id such as ap-south-1"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"state": schema.StringAttribute{
				MarkdownDescription: "`disabled` to stop the firewall operating in this region for " +
					"this account, or `enabled` to override an ancestor that disabled it.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(regionStateEnabled, regionStateDisabled),
				},
			},
			"previous_state": schema.StringAttribute{
				MarkdownDescription: "The state the account had before this resource changed it, " +
					"which is what destroying the resource restores. Unknown for imported " +
					"controls, whose destroy then lifts a disable but never stages one.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path the account resolved to, recomputed on every read.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Status reported by the platform: `ENABLED` or `DISABLED`.",
				Computed:            true,
			},
			"pending": schema.BoolAttribute{
				MarkdownDescription: "True while this region has a staged change that has not been " +
					"submitted and deployed.",
				Computed: true,
			},
			"critical": schema.BoolAttribute{
				MarkdownDescription: "Whether the platform treats this region as critical. A critical " +
					"region cannot be disabled.",
				Computed: true,
			},
			"region_name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name of the region.",
				Computed:            true,
			},
		},
	}
}

func (r *regionControlResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

// resolveScope turns root_scope and resource_id into the canonical account
// scope, rejecting containers -- the same rule and reason as the service
// controls: a container control is a point-in-time expansion that new accounts
// do not join, and the aggregate status hides that gap from Read.
func (r *regionControlResource) resolveScope(ctx context.Context, m *regionControlModel) (string, error) {
	return resolveModelScope(ctx, r.api, m.RootScope, m.ResourceID, &m.Scope)
}

func (r *regionControlResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan regionControlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	staged, diags := r.apply(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !staged {
		return
	}
	// The change was staged, so state is saved even when the refresh after it
	// failed: erroring out without saving would leave a staged control
	// Terraform has no record of.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *regionControlResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state regionControlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	regionID := state.RegionID.ValueString()

	scope, err := r.resolveScope(ctx, &state)
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	region, err := r.api.RegionControl(ctx, scope, regionID)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "region no longer present in account, removing from state",
				map[string]any{"region_id": regionID, "scope": scope})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read region control", err)...)
		return
	}

	reading := regionStateFor(region)

	// A status this provider does not recognize is a provider/platform
	// mismatch, not evidence the control is gone. Dropping the resource here
	// would leave a disabled region with no Terraform record, so the read
	// fails instead.
	if reading == "" {
		resp.Diagnostics.AddError(
			"Unrecognized region status",
			fmt.Sprintf("The platform reports %q for %s at %s, which this provider version does "+
				"not understand. The resource is left in state untouched.",
				region.Status, regionID, scope),
		)
		return
	}

	// Right after an import there is no declared state to compare against;
	// the platform's reading is adopted as the declaration.
	if state.State.IsNull() {
		state.State = types.StringValue(reading)
	} else if reading != state.State.ValueString() {
		tflog.Info(ctx, "region no longer in the declared state, removing from state",
			map[string]any{"region_id": regionID, "scope": scope, "status": region.Status})
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(regionControlID(state))
	state.Status = types.StringValue(region.Status)
	state.Pending = types.BoolValue(region.Pending)
	state.Critical = types.BoolValue(region.Critical)
	state.RegionName = types.StringValue(region.RegionName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *regionControlResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan regionControlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only state can reach here; the rest force replacement. Flipping it is
	// the opposing mutation, which also withdraws an undeployed staged change
	// in the other direction rather than stacking on it. previous_state
	// carries through unchanged, so a flipped-then-destroyed resource still
	// restores the original.
	staged, diags := r.apply(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if !staged {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *regionControlResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state regionControlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	regionID := state.RegionID.ValueString()

	scope, err := r.resolveScope(ctx, &state)
	if err != nil {
		// An account that left the hierarchy took its region controls with it;
		// failing here would leave a resource only `terraform state rm` can
		// clear.
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	current, err := r.api.RegionControl(ctx, scope, regionID)
	if err != nil {
		// The region itself is gone from the account; there is nothing left to
		// restore.
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read region control", err)...)
		return
	}

	action := regionDeleteAction(
		state.State.ValueString(),
		state.PreviousState.ValueString(),
		regionStateFor(current),
	)

	switch action {
	case regionDeleteDrift:
		resp.Diagnostics.AddWarning(
			"Region control already changed outside Terraform",
			fmt.Sprintf("%s at %s no longer reads %s, so nothing is withdrawn. The platform is "+
				"left as it is.", regionID, scope, state.State.ValueString()),
		)
	case regionDeleteEnable:
		if err := r.api.EnableRegion(ctx, regionID, scope); err != nil {
			resp.Diagnostics.Append(diagnoseAPIError("Unable to re-enable region", err)...)
			return
		}
		resp.Diagnostics.AddWarning(
			"Re-enabling the region is staged, not applied",
			fmt.Sprintf("%s has been staged for re-enabling at %s. It remains disabled until the "+
				"pending changes for its root scope are submitted and deployed, which a "+
				"sonrai_control_submission covering that scope does later in this same apply.",
				regionID, scope),
		)
	case regionDeleteDisable:
		// There is no unmanaged state and no usable delete: DeleteStagedRegion
		// takes a staged id the read never returns. The enable this resource
		// recorded over an ancestor disable is withdrawn by disabling again --
		// previous_state proves the disable existed before this resource did.
		if err := r.api.DisableRegion(ctx, regionID, scope); err != nil {
			resp.Diagnostics.Append(diagnoseAPIError("Unable to withdraw the region override", err)...)
			return
		}
		resp.Diagnostics.AddWarning(
			"Withdrawing the region override is staged, not applied",
			fmt.Sprintf("The enable of %s at %s has been staged for removal, restoring the "+
				"ancestor's disable. It remains in force until the pending changes for its root "+
				"scope are submitted and deployed, which a sonrai_control_submission covering that "+
				"scope does later in this same apply.", regionID, scope),
		)
	case regionDeleteKeep:
		// An imported enable override: nothing proves a disable preceded it,
		// and staging one on destroy could switch off a region nobody asked
		// to. Destroy never makes the platform more restrictive on a guess.
		resp.Diagnostics.AddWarning(
			"Imported region override left in place",
			fmt.Sprintf("The enable of %s at %s was imported, so there is no record of what it "+
				"overrode. It remains in force; declare state = \"disabled\" explicitly if the "+
				"region should be disabled.", regionID, scope),
		)
	}
}

// Delete's possible actions, decided by regionDeleteAction.
const (
	regionDeleteDrift   = "drift"
	regionDeleteEnable  = "enable"
	regionDeleteDisable = "disable"
	regionDeleteKeep    = "keep"
)

// regionDeleteAction decides what destroying the resource does: restore the
// recorded previous state, or nothing when the platform moved on without us or
// when restoring would mean guessing.
func regionDeleteAction(declared, previous, current string) string {
	if current != declared {
		return regionDeleteDrift
	}
	switch previous {
	case regionStateEnabled:
		return regionDeleteEnable
	case regionStateDisabled:
		return regionDeleteDisable
	}
	// No recorded previous state: the resource was imported. Lifting a disable
	// only ever widens availability; staging one is never done on a guess.
	if declared == regionStateDisabled {
		return regionDeleteEnable
	}
	return regionDeleteKeep
}

func (r *regionControlResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, importSeparator)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected %q, for example %q, but got %q.",
				"region_id"+importSeparator+"root_scope"+importSeparator+"resource_id",
				"ap-south-1"+importSeparator+"aws/r-abcd"+importSeparator+"123456789012", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("root_scope"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), parts[2])...)
	// state is deliberately left unset: the post-import Read adopts the
	// platform's reading, and previous_state stays unknown, which Delete
	// treats as "never stage a disable on a guess".
}

// apply stages the declared state and confirms it took.
//
// staged reports whether the change reached the platform, so the caller can
// tell a refused mutation from one that was recorded.
func (r *regionControlResource) apply(ctx context.Context, m *regionControlModel) (staged bool, diags diag.Diagnostics) {
	regionID := m.RegionID.ValueString()
	want := m.State.ValueString()

	scope, err := r.resolveScope(ctx, m)
	if err != nil {
		diags.Append(diagnoseResolveError(err)...)
		return false, diags
	}

	// Both mutations answer success without staging anything when there is
	// nothing to act on, so the reasons a change cannot take are checked here
	// where they can be reported against the attribute that caused them.
	before, err := r.api.RegionControl(ctx, scope, regionID)
	if err != nil {
		if isNotFound(err) {
			diags.AddAttributeError(
				path.Root("region_id"),
				"Region is not available in this account",
				fmt.Sprintf("%s is not among the regions enabled for %s, so there is nothing to "+
					"control. Regions come from the cloud account itself; enable it there first.",
					regionID, scope),
			)
			return false, diags
		}
		diags.Append(diagnoseAPIError("Unable to read region control", err)...)
		return false, diags
	}
	if before.Critical && want == regionStateDisabled {
		diags.AddAttributeError(
			path.Root("region_id"),
			"Critical regions cannot be disabled",
			fmt.Sprintf("%s is a critical region. The platform refuses to disable it.", regionID),
		)
		return false, diags
	}

	// A declaration the platform already satisfies outright records a control
	// this configuration never caused -- and destroying it later would mutate
	// state it never owned.
	if reason := regionNoOpReason(before, want); reason != "" {
		diags.AddAttributeError(path.Root("state"), "Region is already in the declared state", reason)
		return false, diags
	}

	// The restore target for a later destroy. Only recorded once: an update
	// that flips the state must still restore the original on destroy.
	if m.PreviousState.IsNull() || m.PreviousState.IsUnknown() {
		m.PreviousState = types.StringValue(regionStateFor(before))
	}
	m.ID = types.StringValue(regionControlID(*m))

	tflog.Info(ctx, "staging region control", map[string]any{
		"region_id": regionID, "scope": scope, "state": want,
	})

	if want == regionStateDisabled {
		err = r.api.DisableRegion(ctx, regionID, scope)
	} else {
		err = r.api.EnableRegion(ctx, regionID, scope)
	}
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to set region control", err)...)
		return false, diags
	}

	after, err := r.api.RegionControl(ctx, scope, regionID)
	if err != nil {
		// The mutation landed; losing the refresh must not lose the record.
		// Computed attributes fall back to the pre-apply reading.
		m.Status = types.StringValue(before.Status)
		m.Pending = types.BoolValue(true)
		m.Critical = types.BoolValue(before.Critical)
		m.RegionName = types.StringValue(before.RegionName)
		diags.Append(diagnoseAPIError("Unable to read region control back", err)...)
		return true, diags
	}
	// The no-op guard above means before never satisfied want, so an after
	// that neither changed nor went pending is a mutation the platform
	// swallowed. Reporting it as applied would leave every later plan
	// proposing it again.
	if regionStateFor(after) != want || (after.Status == before.Status && !after.Pending) {
		diags.AddError(
			"Region control was not staged",
			fmt.Sprintf("The platform accepted the request to set %s to %s at %s without recording "+
				"it: the region still reads %s.", regionID, want, scope, after.Status),
		)
		return false, diags
	}

	m.Status = types.StringValue(after.Status)
	m.Pending = types.BoolValue(after.Pending)
	m.Critical = types.BoolValue(after.Critical)
	m.RegionName = types.StringValue(after.RegionName)
	return true, diags
}

// regionNoOpReason explains why the declared state would change nothing, or
// returns "" when the mutation is a real change.
func regionNoOpReason(before *client.RegionControl, want string) string {
	switch {
	case want == regionStateDisabled && before.Status == client.RegionDisabled:
		return "The region is already disabled, here or through an ancestor scope. Import the " +
			"existing control instead of declaring a new one."
	case want == regionStateEnabled && before.Status == client.RegionEnabled:
		return "The region is already enabled; an enable override is only meaningful where an " +
			"ancestor disables the region. Import the existing control if this account has one."
	}
	return ""
}

// regionStateFor maps a reported status onto the declared state it satisfies.
// An account reads ENABLED or DISABLED; the partial values only appear on
// containers, which this resource does not target, and map conservatively to
// the side they lean.
func regionStateFor(region *client.RegionControl) string {
	switch region.Status {
	case client.RegionDisabled, client.RegionPartiallyDisabled:
		return regionStateDisabled
	case client.RegionEnabled, client.RegionPartiallyEnabled:
		return regionStateEnabled
	default:
		return ""
	}
}

func regionControlID(m regionControlModel) string {
	return controlID(m.RegionID, m.RootScope, m.ResourceID)
}
