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

// The id of an account, organizational unit, root or their GCP and Azure
// equivalents. No "@", which separates the parts of an import id.
var hierarchyResourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9|._:/-]+$`)

type approversResource struct {
	api *client.Client
}

// NewApproversResource returns the sonrai_approvers resource.
func NewApproversResource() resource.Resource { return &approversResource{} }

type approversModel struct {
	ID                   types.String `tfsdk:"id"`
	RootScope            types.String `tfsdk:"root_scope"`
	ResourceID           types.String `tfsdk:"resource_id"`
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
		MarkdownDescription: "Sets who approves access requests at an account, organizational unit or " +
			"root, and who is allowed to.\n\n" +
			"The resource is authoritative for the approvers assigned at its scope: any assigned there " +
			"and left out of `approvers` are removed. Approvers a scope inherits from its parents are " +
			"not affected and are not listed. A scope that already has approvers of its own is rejected " +
			"rather than adopted; import it instead.\n\n" +
			"Unlike the control resources, this takes effect as soon as it is applied. Nothing is staged " +
			"and no `sonrai_control_submission` is involved.\n\n" +
			"Approver exceptions, the conditional approver rules set on a scope in the console, are " +
			"not managed by this resource and are left as they are: every write sends the scope's " +
			"exceptions back unchanged, and destroy keeps them.\n\n" +
			"~> **Applying can invite people and grant a role.** An address that is not yet a Sonrai " +
			"user is sent an invite, and an approver without an approver role is granted " +
			"`CloudPermissionFirewallApprover`. Neither is undone when the approver is removed here.",
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
					stringvalidator.LengthAtMost(1024),
					stringvalidator.RegexMatches(approverScopePattern,
						"must be a scope path beginning with aws/, gcp/ or azure/, with no spaces or trailing slash"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_id": schema.StringAttribute{
				MarkdownDescription: "Cloud-native id of what to set approvers at: an account such as " +
					"`123456789012`, an organizational unit such as `ou-ab12-cdef3456`, or the root " +
					"itself, such as `r-abcd`. Give ids bare. Unlike the control resources, " +
					"organizational units and roots are accepted here.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 256),
					stringvalidator.RegexMatches(hierarchyResourceIDPattern,
						"must be a cloud resource id, not a scope path or an email address"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Canonical scope path `resource_id` resolved to, recomputed on every read.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
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
	// Resolving is also what catches a target that does not exist: the
	// platform accepts an empty approver list for an unknown scope and changes
	// nothing.
	scope, err := r.resolve(ctx, &plan)
	if err != nil {
		resp.Diagnostics.Append(diagnoseApproverTarget(plan, err)...)
		return
	}
	before, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseApproverTarget(plan, err)...)
		return
	}

	// Approvers someone already assigned are rejected rather than adopted:
	// recording them would have destroy remove approvers this configuration
	// never set. The same rule as the control resources.
	if len(before.Emails) > 0 {
		resp.Diagnostics.AddAttributeError(
			path.Root("resource_id"),
			"Scope already has approvers",
			fmt.Sprintf("%s already has %s assigned. Import the existing approvers instead: "+
				"terraform import <address> '%s'", scope, strings.Join(before.Emails, ", "), approversID(plan)),
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
	scope, err := r.resolve(ctx, &state)
	var current *client.ScopeApprovers
	if err == nil {
		current, err = r.api.ScopeApprovers(ctx, scope)
	}
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "scope no longer present, removing approvers from state",
				map[string]any{"resource_id": state.ResourceID.ValueString()})
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
	state.ID = types.StringValue(approversID(state))
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
	scope, err := r.resolve(ctx, &plan)
	if err != nil {
		resp.Diagnostics.Append(diagnoseApproverTarget(plan, err)...)
		return
	}
	before, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseApproverTarget(plan, err)...)
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
	scope, err := r.resolve(ctx, &state)
	var before *client.ScopeApprovers
	if err == nil {
		before, err = r.api.ScopeApprovers(ctx, scope)
	}
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
	if err := r.api.AssignScopeApprovers(ctx, scope, nil, restore, before.Exceptions); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to remove approvers", err)...)
		return
	}
	r.warnIfExceptionsLost(ctx, &resp.Diagnostics, scope, len(before.Exceptions))
}

func (r *approversResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	rootScope, resourceID, ok := strings.Cut(req.ID, importSeparator)
	if !ok || !approverScopePattern.MatchString(rootScope) || !hierarchyResourceIDPattern.MatchString(resourceID) {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected %q, for example %q, but got %q.",
				"root_scope"+importSeparator+"resource_id", "aws/r-abcd"+importSeparator+"123456789012", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("root_scope"), rootScope)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), resourceID)...)
	// Known and empty, not null: nothing is recorded to restore on destroy.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("previous_approval_type"), "")...)
}

// resolve turns root_scope and resource_id into the scope path and records it
// on m. Done on every operation, so an account or organizational unit that
// was moved is followed to its new path.
func (r *approversResource) resolve(ctx context.Context, m *approversModel) (string, error) {
	scope, err := scopeByID(ctx, r.api, m.RootScope, m.ResourceID)
	if err != nil {
		return "", err
	}
	m.Scope = types.StringValue(scope)
	return scope, nil
}

func approversID(m approversModel) string {
	return m.RootScope.ValueString() + importSeparator + m.ResourceID.ValueString()
}

// assign writes the planned approvers and approval type, and fills in the
// computed attributes on m.
func (r *approversResource) assign(ctx context.Context, m *approversModel, exceptions []client.ApproverException) diag.Diagnostics {
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

	if err := r.api.AssignScopeApprovers(ctx, scope, emails, approvalType, exceptions); err != nil {
		diags.Append(diagnoseAPIError("Unable to assign approvers", err)...)
		return diags
	}
	m.ID = types.StringValue(approversID(*m))
	if m.PreviousApprovalType.IsNull() || m.PreviousApprovalType.IsUnknown() {
		m.PreviousApprovalType = types.StringValue("")
	}
	r.warnIfExceptionsLost(ctx, &diags, scope, len(exceptions))
	return diags
}

// warnIfExceptionsLost reads the scope back after a write that was meant to
// keep its exceptions. The write replaces them with what it was sent, so a
// platform that dropped or rejected part of that would lose them silently.
func (r *approversResource) warnIfExceptionsLost(ctx context.Context, diags *diag.Diagnostics, scope string, before int) {
	if before == 0 {
		return
	}
	after, err := r.api.ScopeApprovers(ctx, scope)
	if err != nil {
		diags.AddWarning("Unable to confirm approver exceptions were kept",
			fmt.Sprintf("%s had %d approver exception(s) before this write, and reading it back failed: %s. "+
				"Check them in the console.", scope, before, err.Error()))
		return
	}
	warnExceptionsRemoved(diags, scope, before-len(after.Exceptions))
}

func warnExceptionsRemoved(diags *diag.Diagnostics, scope string, lost int) {
	if lost <= 0 {
		return
	}
	noun := "exceptions were"
	if lost == 1 {
		noun = "exception was"
	}
	diags.AddWarning(
		fmt.Sprintf("%d approver %s removed from %s", lost, noun, scope),
		"sonrai_approvers sends a scope's approver exceptions back unchanged with every write, but the "+
			"platform did not keep all of them this time. Recreate the missing ones in the console.",
	)
}

func diagnoseApproverTarget(m approversModel, err error) diag.Diagnostics {
	if isNotFound(err) {
		return diagnoseScopeByID(m.RootScope, m.ResourceID, err)
	}
	return diagnoseAPIError("Unable to read approvers", err)
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
