package provider

import (
	"context"
	"fmt"
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

var (
	_ resource.Resource                = (*organizationPolicyAttachmentResource)(nil)
	_ resource.ResourceWithConfigure   = (*organizationPolicyAttachmentResource)(nil)
	_ resource.ResourceWithImportState = (*organizationPolicyAttachmentResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*organizationPolicyAttachmentResource)(nil)
)

type organizationPolicyAttachmentResource struct {
	api *client.Client
}

// NewOrganizationPolicyAttachmentResource returns the
// sonrai_organization_policy_attachment resource.
func NewOrganizationPolicyAttachmentResource() resource.Resource {
	return &organizationPolicyAttachmentResource{}
}

type organizationPolicyAttachmentModel struct {
	ID         types.String `tfsdk:"id"`
	PolicyID   types.String `tfsdk:"policy_id"`
	RootScope  types.String `tfsdk:"root_scope"`
	ResourceID types.String `tfsdk:"resource_id"`
	Scope      types.String `tfsdk:"scope"`
	Revision   types.String `tfsdk:"revision"`
}

func (r *organizationPolicyAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_policy_attachment"
}

func (r *organizationPolicyAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches an organization policy to an account, organizational unit or the " +
			"root of an AWS organization. The policy then applies there and to everything beneath, " +
			"including accounts added later.\n\n" +
			"Attaching and detaching are staged. Neither is in force until the root scope is submitted " +
			"and deployed, so reference `revision` in the `triggers` of its " +
			"`sonrai_control_submission`.\n\n" +
			"A policy that already reaches the target is rejected rather than adopted; import the " +
			"attachment instead.\n\n" +
			"~> **A scope the policy reaches from above looks attached.** The platform records the " +
			"policy at every scope beneath an attachment and does not say which one it was attached at. " +
			"An attachment to an account whose organizational unit is also attached therefore cannot be " +
			"told apart from inheritance: it reads as present for as long as the unit's does, and the " +
			"platform refuses to detach it while the unit's remains unless it was attached there " +
			"directly. Attach at one level of a branch, not several.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `policy_id@root_scope@resource_id`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"policy_id": schema.StringAttribute{
				MarkdownDescription: "The `id` of the `sonrai_organization_policy` to attach. Forces replacement.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(policyIDPattern, "must be an organization policy id"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Root scope of the AWS organization, for example `aws/r-abcd`. Forces replacement.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(1024),
					stringvalidator.RegexMatches(awsRootScopePattern,
						"must be an AWS root scope such as aws/r-abcd; organization policies are AWS only"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_id": schema.StringAttribute{
				MarkdownDescription: "AWS id of what to attach the policy to: an account such as " +
					"`123456789012`, an organizational unit such as `ou-ab12-cdef3456`, or the root " +
					"itself, such as `r-abcd`. Forces replacement.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 256),
					stringvalidator.RegexMatches(hierarchyResourceIDPattern, "must be an AWS id, not a scope path"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Scope path `resource_id` resolved to, recomputed on every read.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"revision": revisionAttribute(),
		},
	}
}

func (r *organizationPolicyAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

// ModifyPlan resolves the target while the plan can still reject it, and
// keeps revision still when nothing is going to be staged.
func (r *organizationPolicyAttachmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.api == nil || req.Plan.Raw.IsNull() {
		return
	}
	var plan organizationPolicyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !req.State.Raw.IsNull() {
		var state organizationPolicyAttachmentModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		// Every argument forces replacement, so a plan that keeps the resource
		// stages nothing.
		if plan.PolicyID.Equal(state.PolicyID) && plan.RootScope.Equal(state.RootScope) && plan.ResourceID.Equal(state.ResourceID) {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("revision"), state.Revision)...)
			return
		}
	}
	if plan.RootScope.IsUnknown() || plan.ResourceID.IsUnknown() {
		return
	}
	scope, err := scopeByID(ctx, r.api, plan.RootScope, plan.ResourceID)
	if err != nil {
		resp.Diagnostics.Append(diagnoseScopeByID(plan.RootScope, plan.ResourceID, err)...)
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("scope"), scope)...)
	if !plan.PolicyID.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), attachmentID(plan))...)
	}
}

func (r *organizationPolicyAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationPolicyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policyID := plan.PolicyID.ValueString()

	scope, err := scopeByID(ctx, r.api, plan.RootScope, plan.ResourceID)
	if err != nil {
		resp.Diagnostics.Append(diagnoseScopeByID(plan.RootScope, plan.ResourceID, err)...)
		return
	}
	detail, err := r.api.OrganizationPolicy(ctx, policyID)
	if err != nil {
		resp.Diagnostics.Append(diagnosePolicyForAttachment(policyID, err)...)
		return
	}

	// A policy that already reaches the scope is rejected rather than adopted:
	// recording it would have destroy detach something this configuration
	// never attached. The same rule as the other resources.
	if detail.AttachedAt(scope) {
		resp.Diagnostics.AddAttributeError(path.Root("resource_id"), "Organization policy already reaches this scope",
			fmt.Sprintf("%q is already attached at %s, or reaches it from a scope above. If it was attached "+
				"here directly, import the attachment instead: terraform import <address> '%s'. If it is "+
				"inherited, this attachment adds nothing; remove it, or detach the policy above first.",
				detail.Policy.Name, scope, attachmentID(plan)))
		return
	}

	tflog.Info(ctx, "staging organization policy attachment", map[string]any{"policy_id": policyID, "scope": scope})
	if err := r.api.AttachOrganizationPolicy(ctx, policyID, scope); err != nil {
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to attach organization policy", err)...)
		return
	}

	plan.Scope = types.StringValue(scope)
	plan.ID = types.StringValue(attachmentID(plan))
	plan.Revision = newRevision()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *organizationPolicyAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationPolicyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policyID := state.PolicyID.ValueString()

	scope, err := scopeByID(ctx, r.api, state.RootScope, state.ResourceID)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "attachment target no longer in the hierarchy, removing from state",
				map[string]any{"resource_id": state.ResourceID.ValueString()})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseScopeByID(state.RootScope, state.ResourceID, err)...)
		return
	}
	detail, err := r.api.OrganizationPolicy(ctx, policyID)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "organization policy no longer present, removing attachment from state",
				map[string]any{"policy_id": policyID})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to read organization policy", err)...)
		return
	}
	if !detail.AttachedAt(scope) {
		tflog.Info(ctx, "organization policy no longer attached, removing from state",
			map[string]any{"policy_id": policyID, "scope": scope})
		resp.State.RemoveResource(ctx)
		return
	}

	state.Scope = types.StringValue(scope)
	state.ID = types.StringValue(attachmentID(state))
	ensureRevision(&state.Revision)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update has nothing to write: every argument forces replacement.
func (r *organizationPolicyAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state organizationPolicyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *organizationPolicyAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationPolicyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	policyID := state.PolicyID.ValueString()

	// Detached where it was recorded when the target has since left the
	// hierarchy: there is nowhere else to look for it.
	scope, err := scopeByID(ctx, r.api, state.RootScope, state.ResourceID)
	if err != nil {
		if !isNotFound(err) {
			resp.Diagnostics.Append(diagnoseScopeByID(state.RootScope, state.ResourceID, err)...)
			return
		}
		scope = state.Scope.ValueString()
	}

	detail, err := r.api.OrganizationPolicy(ctx, policyID)
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to read organization policy", err)...)
		return
	}
	deployed := detail.DeployedAt(scope)

	tflog.Info(ctx, "staging organization policy detachment", map[string]any{"policy_id": policyID, "scope": scope})
	if err := r.api.DetachOrganizationPolicy(ctx, policyID, scope); err != nil {
		if isNotFound(err) {
			return
		}
		if strings.Contains(err.Error(), "attached at a parent scope") {
			resp.Diagnostics.AddError("Organization policy is attached above this scope",
				fmt.Sprintf("The policy reaches %s from a scope above it, where it is still attached, so it "+
					"cannot be detached here on its own. Detach it from the scope above first. If this "+
					"attachment was only ever inherited, remove it from state instead: "+
					"terraform state rm <address>.\n\nUnderlying error: %s", scope, err.Error()))
			return
		}
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to detach organization policy", err)...)
		return
	}
	if deployed {
		resp.Diagnostics.AddWarning("Detaching the organization policy is staged, not applied",
			policyDetachStagedDetail(detail.Policy.Name, scope))
	}
}

func policyDetachStagedDetail(name, scope string) string {
	return fmt.Sprintf("The attachment of %q at %s has been staged for removal. The policy remains in force "+
		"there until the pending changes for its root scope are submitted and deployed, which a "+
		"sonrai_control_submission covering that scope does later in this same apply.", name, scope)
}

func (r *organizationPolicyAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	policyID, rest, ok := strings.Cut(req.ID, importSeparator)
	rootScope, resourceID, ok2 := strings.Cut(rest, importSeparator)
	if !ok || !ok2 || !policyIDPattern.MatchString(policyID) || !awsRootScopePattern.MatchString(rootScope) ||
		!hierarchyResourceIDPattern.MatchString(resourceID) {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected %q, for example %q, but got %q.",
				"policy_id"+importSeparator+"root_scope"+importSeparator+"resource_id",
				"a1b2c3d4-0000-4000-8000-000000000000"+importSeparator+"aws/r-abcd"+importSeparator+"123456789012",
				req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), strings.ToLower(policyID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("root_scope"), rootScope)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), resourceID)...)
}

func attachmentID(m organizationPolicyAttachmentModel) string {
	return m.PolicyID.ValueString() + importSeparator + m.RootScope.ValueString() + importSeparator + m.ResourceID.ValueString()
}

func diagnosePolicyForAttachment(policyID string, err error) diag.Diagnostics {
	if isNotFound(err) {
		var diags diag.Diagnostics
		diags.AddAttributeError(path.Root("policy_id"), "Organization policy not found",
			fmt.Sprintf("No managed organization policy has the id %q. Use the id of a "+
				"sonrai_organization_policy; a policy Sonrai only discovered in AWS cannot be attached "+
				"until it is managed.", policyID))
		return diags
	}
	return diagnoseOrganizationPolicyError("Unable to read organization policy", err)
}
