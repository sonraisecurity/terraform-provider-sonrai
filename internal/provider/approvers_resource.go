package provider

import (
	"context"
	"fmt"
	"regexp"
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
	_ resource.Resource                = (*approversResource)(nil)
	_ resource.ResourceWithConfigure   = (*approversResource)(nil)
	_ resource.ResourceWithImportState = (*approversResource)(nil)
)

// maxApprovers bounds one scope's list. The platform sets no limit of its own;
// this one is far above any real approver list and stops a templating mistake
// from inviting a directory's worth of people.
const maxApprovers = 100

// Lower case only. The platform matches approvers case-insensitively and
// reports them as stored, so a mixed-case address in configuration would never
// read back equal to itself.
var approverEmailPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._%+'-]*@[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// A scope path with nothing that could be read as anything else: no empty
// segments, no whitespace, no trailing slash.
var approverScopePattern = regexp.MustCompile(`^(aws|gcp|azure)(/[A-Za-z0-9|._:-]+)+$`)

type approversResource struct {
	api *client.Client
}

// NewApproversResource returns the sonrai_approvers resource.
func NewApproversResource() resource.Resource { return &approversResource{} }

type approversModel struct {
	ID                   types.String `tfsdk:"id"`
	Scope                types.String `tfsdk:"scope"`
	Approvers            types.Set    `tfsdk:"approvers"`
	ApprovalType         types.String `tfsdk:"approval_type"`
	PreviousApprovalType types.String `tfsdk:"previous_approval_type"`
}

func (r *approversResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_approvers"
}

func (r *approversResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets who approves access requests at a scope, and who is allowed to.\n\n" +
			"The resource is authoritative for the approvers assigned at its scope: any assigned there " +
			"and left out of `approvers` are removed. Approvers a scope inherits from its parents are " +
			"not affected and are not listed. A scope that already has approvers of its own is rejected " +
			"rather than adopted; import it instead.\n\n" +
			"Unlike the control resources, this takes effect as soon as it is applied. Nothing is staged " +
			"and no `sonrai_control_submission` is involved.\n\n" +
			"~> **Approver exceptions are removed.** Conditional approver rules set on the scope in the " +
			"console are not managed by this resource yet, and the platform deletes them whenever the " +
			"scope's approvers are written. Every apply that creates, changes or destroys this resource " +
			"removes them, and the apply warns when it does.\n\n" +
			"~> **Applying can invite people and grant a role.** An address that is not yet a Sonrai " +
			"user is sent an invite, and an approver without an approver role is granted " +
			"`CloudPermissionFirewallApprover`. Neither is undone when the approver is removed here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `scope`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Scope path to set approvers at: a root scope such as `aws/r-abcd`, " +
					"an organizational unit, or an account such as `aws/r-abcd/ou-ab12-cdef3456/123456789012`.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(1024),
					stringvalidator.RegexMatches(approverScopePattern,
						"must be a scope path beginning with aws/, gcp/ or azure/, with no spaces or trailing slash"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"approvers": schema.SetAttribute{
				MarkdownDescription: "Email addresses of the users and groups that approve requests at this " +
					"scope, in lower case. May be empty, which leaves the scope to the approvers it " +
					fmt.Sprintf("inherits. At most %d.", maxApprovers),
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeAtMost(maxApprovers),
					setvalidator.ValueStringsAre(
						stringvalidator.LengthAtMost(254),
						stringvalidator.RegexMatches(approverEmailPattern, "must be an email address in lower case"),
					),
				},
			},
			"approval_type": schema.StringAttribute{
				MarkdownDescription: "Who may approve a request at this scope.\n\n" +
					"  - `exclude_self`: the assigned approvers, but never their own request.\n" +
					"  - `assigned_approvers`: the assigned approvers, their own requests included.\n" +
					"  - `everyone_at_scope`: anyone, their own requests included.\n\n" +
					"The platform also writes this to every scope beneath this one that has no approvers " +
					"of its own, so a `sonrai_approvers` on such a scope will report the change as drift.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(approvalTypeNames()...),
				},
			},
			"previous_approval_type": schema.StringAttribute{
				MarkdownDescription: "The approval type the scope had before this resource, which destroy " +
					"restores. Empty after an import, in which case destroy leaves the approval type as it is.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *approversResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *approversResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan approversModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := plan.Scope.ValueString()

	// Also what catches a scope that does not exist: the platform accepts an
	// empty approver list for an unknown scope and changes nothing.
	before, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseApproverScope("Unable to read approvers", scope, err)...)
		return
	}

	// Approvers someone already assigned are rejected rather than adopted:
	// recording them would have destroy remove approvers this configuration
	// never set. The same rule as the control resources.
	if len(before.Emails) > 0 {
		resp.Diagnostics.AddAttributeError(
			path.Root("scope"),
			"Scope already has approvers",
			fmt.Sprintf("%s already has %s assigned. Import the existing approvers instead: "+
				"terraform import <address> '%s'", scope, strings.Join(before.Emails, ", "), scope),
		)
		return
	}

	plan.PreviousApprovalType = types.StringValue(approvalTypeName(before.ApprovalType))
	resp.Diagnostics.Append(r.assign(ctx, &plan, before.Exceptions)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *approversResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state approversModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := state.Scope.ValueString()

	current, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "scope no longer present, removing approvers from state", map[string]any{"scope": scope})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read approvers", err)...)
		return
	}

	approvers, diags := stringsToSet(ctx, current.Emails)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.ID = types.StringValue(scope)
	state.Approvers = approvers
	state.ApprovalType = types.StringValue(approvalTypeName(current.ApprovalType))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *approversResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan approversModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := plan.Scope.ValueString()

	before, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseApproverScope("Unable to read approvers", scope, err)...)
		return
	}
	// The write replaces the whole set, so resending it in full is the update.
	resp.Diagnostics.Append(r.assign(ctx, &plan, before.Exceptions)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *approversResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state approversModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := state.Scope.ValueString()

	before, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		// A scope that left the hierarchy took its approvers with it.
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read approvers", err)...)
		return
	}

	// An imported resource has no record of what came before it, and an empty
	// approval type tells the platform to keep the one it has.
	restore := ""
	if previous := state.PreviousApprovalType.ValueString(); previous != "" {
		restore = approvalTypeAPI(previous)
	}

	tflog.Info(ctx, "removing approvers", map[string]any{"scope": scope, "restore_approval_type": restore})
	if err := r.api.AssignScopeApprovers(ctx, scope, nil, restore); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to remove approvers", err)...)
		return
	}
	warnExceptionsRemoved(&resp.Diagnostics, scope, before.Exceptions)
}

func (r *approversResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !approverScopePattern.MatchString(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected a scope, for example %q, but got %q.", "aws/r-abcd/123456789012", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("scope"), req.ID)...)
	// Known and empty, not null: nothing is recorded to restore on destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("previous_approval_type"), "")...)
}

// assign writes the planned approvers and approval type, and fills in the
// computed attributes on m.
func (r *approversResource) assign(ctx context.Context, m *approversModel, exceptions int) diag.Diagnostics {
	var diags diag.Diagnostics

	emails, d := setToStrings(ctx, m.Approvers)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}

	scope := m.Scope.ValueString()
	approvalType := approvalTypeAPI(m.ApprovalType.ValueString())
	tflog.Info(ctx, "assigning approvers", map[string]any{
		"scope": scope, "approvers": len(emails), "approval_type": approvalType,
	})

	if err := r.api.AssignScopeApprovers(ctx, scope, emails, approvalType); err != nil {
		diags.Append(diagnoseAPIError("Unable to assign approvers", err)...)
		return diags
	}
	m.ID = types.StringValue(scope)
	if m.PreviousApprovalType.IsNull() || m.PreviousApprovalType.IsUnknown() {
		m.PreviousApprovalType = types.StringValue("")
	}
	warnExceptionsRemoved(&diags, scope, exceptions)
	return diags
}

// warnExceptionsRemoved reports the exceptions a write just deleted. The
// platform removes them on every write to a scope's approvers and this
// resource does not put them back, so saying nothing would lose them silently.
func warnExceptionsRemoved(diags *diag.Diagnostics, scope string, exceptions int) {
	if exceptions == 0 {
		return
	}
	noun := "exceptions were"
	if exceptions == 1 {
		noun = "exception was"
	}
	diags.AddWarning(
		fmt.Sprintf("%d approver %s removed from %s", exceptions, noun, scope),
		"The platform deletes a scope's approver exceptions whenever its approvers are written, and "+
			"sonrai_approvers does not manage exceptions yet. Recreate them in the console if they are "+
			"still needed; the next apply that changes this resource will remove them again.",
	)
}

func diagnoseApproverScope(summary, scope string, err error) diag.Diagnostics {
	if isNotFound(err) {
		var diags diag.Diagnostics
		diags.AddAttributeError(path.Root("scope"), "Scope not found",
			fmt.Sprintf("%s is not a scope in the cloud hierarchy. Give the full path of a root, "+
				"organizational unit or account, as the sonrai_scope data source reports it.", scope))
		return diags
	}
	return diagnoseAPIError(summary, err)
}

// Configuration spells approval types in lower case, like every other
// enumerated value in this provider; the API spells them in upper case.
func approvalTypeName(api string) string { return strings.ToLower(api) }

func approvalTypeAPI(name string) string { return strings.ToUpper(name) }

func approvalTypeNames() []string {
	names := make([]string, 0, len(client.ApprovalTypes))
	for _, api := range client.ApprovalTypes {
		names = append(names, approvalTypeName(api))
	}
	return names
}
