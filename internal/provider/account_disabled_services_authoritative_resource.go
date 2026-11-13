package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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

var (
	_ resource.Resource                = (*disabledServicesAuthoritativeResource)(nil)
	_ resource.ResourceWithConfigure   = (*disabledServicesAuthoritativeResource)(nil)
	_ resource.ResourceWithImportState = (*disabledServicesAuthoritativeResource)(nil)
)

type disabledServicesAuthoritativeResource struct {
	api *client.Client
}

// NewDisabledServicesAuthoritativeResource returns the sonrai_account_disabled_services_authoritative resource.
func NewDisabledServicesAuthoritativeResource() resource.Resource {
	return &disabledServicesAuthoritativeResource{}
}

type disabledServicesAuthoritativeModel struct {
	ID          types.String `tfsdk:"id"`
	RootScope   types.String `tfsdk:"root_scope"`
	ResourceID  types.String `tfsdk:"resource_id"`
	ControlKeys types.Set    `tfsdk:"control_keys"`
	Scope       types.String `tfsdk:"scope"`
}

func (r *disabledServicesAuthoritativeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_account_disabled_services_authoritative"
}

func (r *disabledServicesAuthoritativeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Authoritative set of disabled services for one account.\n\n" +
			"**This resource owns every disabled service in the account.** Any service disabled in the " +
			"account but absent from `control_keys` is re-enabled on the next apply, including changes " +
			"made from the console, by another configuration, or by automation. Use " +
			"`sonrai_account_disabled_service` instead to manage individual services without claiming " +
			"the rest.\n\n" +
			"Import the account before first use to see what it already has.\n\n" +
			"Changes are staged and take effect once submitted and deployed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `root_scope@resource_id`.",
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
				MarkdownDescription: "Cloud-native id of the account: an AWS account id, a GCP project id, " +
					"or an Azure subscription id. Organizational units, folders, management groups and " +
					"roots are not accepted.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"control_keys": schema.SetAttribute{
				MarkdownDescription: "Firewall control keys to disable, such as `cloud9` or " +
					"`sagemaker`. Services disabled in this account but absent from this set are " +
					"re-enabled.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path the account resolved to, recomputed on every read.",
				Computed:            true,
			},
		},
	}
}

func (r *disabledServicesAuthoritativeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *disabledServicesAuthoritativeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan disabledServicesAuthoritativeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, err := r.resolveScope(ctx, &plan)
	if err != nil {
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	desired, diags := setToStrings(ctx, plan.ControlKeys)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Authoritative: whatever is blocked here and not declared is removed, so a
	// first apply converges the account rather than merging with what it finds.
	current, err := r.api.BlockedServices(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read disabled services", err)...)
		return
	}

	// State is saved even when reconciling only half-succeeded: a bulk call can
	// stage some disables and fail on others, and erroring out without saving
	// would leave staged controls Terraform has no record of.
	resp.Diagnostics.Append(r.reconcile(ctx, scope, current, desired)...)

	plan.ID = types.StringValue(authoritativeID(plan))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *disabledServicesAuthoritativeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state disabledServicesAuthoritativeModel
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

	blocked, err := r.api.BlockedServices(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read disabled services", err)...)
		return
	}

	keys, diags := stringsToSet(ctx, blocked)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.ControlKeys = keys
	state.ID = types.StringValue(authoritativeID(state))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *disabledServicesAuthoritativeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan disabledServicesAuthoritativeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, err := r.resolveScope(ctx, &plan)
	if err != nil {
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}

	desired, diags := setToStrings(ctx, plan.ControlKeys)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Diff against the server rather than prior state, so blocks added outside
	// Terraform since the last apply are removed as the resource promises.
	current, err := r.api.BlockedServices(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read disabled services", err)...)
		return
	}

	// Saved even on a half-failed reconcile, for the same reason as Create.
	resp.Diagnostics.Append(r.reconcile(ctx, scope, current, desired)...)

	plan.ID = types.StringValue(authoritativeID(plan))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *disabledServicesAuthoritativeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state disabledServicesAuthoritativeModel
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

	declared, diags := setToStrings(ctx, state.ControlKeys)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	applied, inForce := wasApplied(ctx, r.api, scope, declared...)

	if err := r.api.UnmanageServices(ctx, declared, scope); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to re-enable services", err)...)
		return
	}

	if applied {
		resp.Diagnostics.AddWarning(
			"Re-enabling the services is staged, not applied",
			fmt.Sprintf("Disables for %v at %s have been staged for removal. They remain in force "+
				"until the pending changes for their root scope are submitted and deployed, which "+
				"a sonrai_control_submission covering that scope does later in this same apply.",
				inForce, scope),
		)
	}
}

func (r *disabledServicesAuthoritativeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rootScope, resourceID, ok := strings.Cut(req.ID, importSeparator)
	if !ok || rootScope == "" || resourceID == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected %q, for example %q, but got %q.",
				"root_scope"+importSeparator+"resource_id",
				"aws/r-abcd"+importSeparator+"123456789012", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("root_scope"), rootScope)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), resourceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("control_keys"), []string{})...)
}

// reconcile stages whatever is needed to turn current into desired.
func (r *disabledServicesAuthoritativeResource) reconcile(ctx context.Context, scope string, current, desired []string) (diags diag.Diagnostics) {
	add := difference(desired, current)
	remove := difference(current, desired)

	tflog.Info(ctx, "reconciling account disabled services", map[string]any{
		"scope": scope, "add": add, "remove": remove,
	})

	if err := r.api.DisableServices(ctx, add, scope); err != nil {
		diags.Append(diagnoseAPIError("Unable to disable services", err)...)
		return diags
	}
	if err := r.api.UnmanageServices(ctx, remove, scope); err != nil {
		diags.Append(diagnoseAPIError("Unable to re-enable services", err)...)
	}
	return diags
}

func authoritativeID(m disabledServicesAuthoritativeModel) string {
	return m.RootScope.ValueString() + importSeparator + m.ResourceID.ValueString()
}

// difference returns the members of a that are not in b.
func difference(a, b []string) []string {
	inB := make(map[string]struct{}, len(b))
	for _, v := range b {
		inB[v] = struct{}{}
	}
	var out []string
	for _, v := range a {
		if _, ok := inB[v]; !ok {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
