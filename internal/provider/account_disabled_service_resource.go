package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// importSeparator splits control key from scope in an import ID. Scopes contain
// "/" and "|" already, so neither can serve as the separator.
const importSeparator = "@"

var (
	_ resource.Resource                = (*accountDisabledServiceResource)(nil)
	_ resource.ResourceWithConfigure   = (*accountDisabledServiceResource)(nil)
	_ resource.ResourceWithImportState = (*accountDisabledServiceResource)(nil)
)

type accountDisabledServiceResource struct {
	api *client.Client
}

// NewAccountDisabledServiceResource returns the sonrai_account_disabled_service resource.
func NewAccountDisabledServiceResource() resource.Resource { return &accountDisabledServiceResource{} }

type accountDisabledServiceModel struct {
	ID         types.String `tfsdk:"id"`
	RootScope  types.String `tfsdk:"root_scope"`
	ResourceID types.String `tfsdk:"resource_id"`
	ControlKey types.String `tfsdk:"control_key"`
	Scope      types.String `tfsdk:"scope"`
	Status     types.String `tfsdk:"status"`
	Pending    types.Bool   `tfsdk:"pending"`
}

func (r *accountDisabledServiceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_account_disabled_service"
}

func (r *accountDisabledServiceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Disables a cloud service in one account, denying its sensitive " +
			"permissions.\n\n" +
			"A service already disabled -- from the console, or by another configuration -- is " +
			"rejected rather than adopted.\n\n" +
			"This resource claims only the service it names, and never re-enables a service it " +
			"does not manage.\n\n" +
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
				MarkdownDescription: "Cloud-native id of the account to disable the service in: an AWS " +
					"account id, a GCP project id, or an Azure subscription id. Give ids bare; " +
					"`my-project` resolves the same as `projects|my-project`.\n\n" +
					"Organizational units, folders, management groups and roots are not accepted. " +
					"List the member accounts individually.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"control_key": schema.StringAttribute{
				MarkdownDescription: "Firewall control key for the service, for example `s3`, `ec2` or `sagemaker`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path this resolved to, recomputed on every read.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Control status reported by the platform: `Disabled`, `Protected`, " +
					"`Unprotected` or `Non-Privileged`. Reads `Disabled` from the moment a block is staged.",
				Computed: true,
			},
			"pending": schema.BoolAttribute{
				MarkdownDescription: "True while this control has a staged change that has not been submitted and deployed.",
				Computed:            true,
			},
		},
	}
}

func (r *accountDisabledServiceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *accountDisabledServiceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan accountDisabledServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope, err := r.resolveScope(ctx, &plan)
	if err != nil {
		resp.Diagnostics.Append(diagnoseResolveError(err)...)
		return
	}
	controlKey := plan.ControlKey.ValueString()

	// A service someone already disabled is rejected rather than adopted:
	// recording it would have destroy remove a control this configuration
	// never caused. The same rule as region controls.
	if status, err := r.api.ServiceControlStatus(ctx, controlKey, scope); err == nil &&
		status.Status == client.StatusDisabled {
		resp.Diagnostics.AddAttributeError(
			path.Root("control_key"),
			"Service is already disabled",
			fmt.Sprintf("%s at %s is already disabled. Import the existing control instead: "+
				"terraform import <address> '%s'", controlKey, scope, buildID(plan)),
		)
		return
	}

	tflog.Info(ctx, "staging service disable", map[string]any{"control_key": controlKey, "scope": scope})
	if err := r.api.DisableService(ctx, controlKey, scope); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to disable service", err)...)
		return
	}

	plan.ID = types.StringValue(buildID(plan))
	// The block was staged, so state is saved even when the refresh below
	// fails: erroring out without saving would leave a staged control that
	// Terraform has no record of and cannot later remove.
	gone, diags := r.readInto(ctx, &plan)
	if gone {
		diags.AddError("Unable to read service control status",
			fmt.Sprintf("%s at %s disappeared while its disable was being staged.", controlKey, scope))
	}
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountDisabledServiceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state accountDisabledServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	gone, diags := r.readInto(ctx, &state)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// status tracks the staged intent, not just what is deployed: a disable
	// reads Disabled from the moment it is staged. So anything other than
	// Disabled means this control is gone -- re-enabled, or replaced by a
	// different control such as a protect. Drop it from state so the plan
	// offers to put it back, rather than silently reporting no changes while
	// the service is no longer disabled.
	if !disabledServiceStillManaged(state.Status.ValueString()) {
		tflog.Info(ctx, "service disable no longer present, removing from state", map[string]any{
			"control_key": state.ControlKey.ValueString(),
			"scope":       state.Scope.ValueString(),
			"status":      state.Status.ValueString(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *accountDisabledServiceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan accountDisabledServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Every configurable attribute forces replacement, so Terraform never calls
	// this. It exists to satisfy the interface and to refresh computed values if
	// that ever stops being true.
	plan.ID = types.StringValue(buildID(plan))
	gone, diags := r.readInto(ctx, &plan)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountDisabledServiceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state accountDisabledServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve again rather than trusting the stored path: the target may have
	// been reparented since the last apply, and unmanaging a stale scope would
	// leave the real control in place.
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
		resp.Diagnostics.Append(diagnoseAPIError("Unable to re-enable service", err)...)
		return
	}

	if applied {
		resp.Diagnostics.AddWarning(
			"Re-enabling the service is staged, not applied",
			"The disable for "+controlKey+" at "+scope+" has been staged for removal. It remains in "+
				"force until the pending changes for its root scope are submitted and deployed, which "+
				"a sonrai_control_submission covering that scope does later in this same apply.",
		)
	}
}

func (r *accountDisabledServiceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importControlID(ctx, req, resp, "s3"+importSeparator+"aws/r-abcd"+importSeparator+"123456789012")
}

// disabledServiceStillManaged reports whether status still describes the block
// this resource manages. Anything else means it was re-enabled or replaced by
// a different control type.
func disabledServiceStillManaged(status string) bool {
	return status == client.StatusDisabled
}

func buildID(m accountDisabledServiceModel) string {
	return controlID(m.ControlKey, m.RootScope, m.ResourceID)
}
