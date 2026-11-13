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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

// defaultResetWaitTimeout covers a reset across a large organization. The
// transaction only starts its own work once the CloudFormation stack is gone.
const defaultResetWaitTimeout = "2h"

var (
	_ resource.Resource                   = (*controlResetResource)(nil)
	_ resource.ResourceWithConfigure      = (*controlResetResource)(nil)
	_ resource.ResourceWithValidateConfig = (*controlResetResource)(nil)
)

type controlResetResource struct {
	api *client.Client
}

// NewControlResetResource returns the sonrai_control_reset resource.
func NewControlResetResource() resource.Resource { return &controlResetResource{} }

type controlResetModel struct {
	ID            types.String `tfsdk:"id"`
	RootScope     types.String `tfsdk:"root_scope"`
	Confirm       types.String `tfsdk:"confirm"`
	Wait          types.Bool   `tfsdk:"wait"`
	WaitTimeout   types.String `tfsdk:"wait_timeout"`
	TransactionID types.String `tfsdk:"transaction_id"`
	Status        types.String `tfsdk:"status"`
	StatusMessage types.String `tfsdk:"status_message"`
}

func (r *controlResetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_control_reset"
}

func (r *controlResetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rolls every control under a root scope back to staged.\n\n" +
			"**This is destructive and organization-wide.** It returns applied controls to staged, " +
			"stages quarantined identities for removal, and re-stages JIT permission sets and levels " +
			"for re-creation. It does not distinguish between controls Terraform manages and controls " +
			"created from the console or by automation.\n\n" +
			"The reset cannot finish while the firewall's CloudFormation stack still exists: the " +
			"transaction hands off to the cloud and waits for the stack to go. Order the two with " +
			"`depends_on`, so one apply deletes the stack and then runs the reset.\n\n" +
			"```hcl\n" +
			"resource \"sonrai_control_reset\" \"firewall\" {\n" +
			"  count = var.reset ? 1 : 0\n\n" +
			"  root_scope = var.root_scope\n" +
			"  confirm    = \"aws/r-abcd\"\n\n" +
			"  depends_on = [aws_cloudformation_stack.firewall]\n" +
			"}\n" +
			"```\n\n" +
			"Terraform state stays accurate afterwards. The controls remain staged and report the " +
			"same status, so the next plan shows no changes, and clearing `reset` deploys them again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Transaction id of this reset.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope to reset, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"confirm": schema.StringAttribute{
				MarkdownDescription: "Must repeat `root_scope` exactly, and is checked when the plan is " +
					"built rather than when it is applied. Without this an organization's controls would " +
					"be cleared by a single variable. Write the scope out by hand: pointing both " +
					"attributes at the same variable makes the check satisfy itself.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"wait": schema.BoolAttribute{
				MarkdownDescription: "Whether to wait for the reset to complete before the apply returns. " +
					"Defaults to true. Setting it false returns as soon as the transaction starts, leaving " +
					"the reset to finish unobserved.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"wait_timeout": schema.StringAttribute{
				MarkdownDescription: "How long to wait, as a Go duration such as `2h`. Defaults to `" +
					defaultResetWaitTimeout + "`, and cannot exceed `24h`, the point at which a " +
					"transaction expires.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(defaultResetWaitTimeout),
				Validators: []validator.String{
					durationValidator{},
				},
			},
			"transaction_id": schema.StringAttribute{
				MarkdownDescription: "Transaction id returned by the platform.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Transaction status reported by the platform.",
				Computed:            true,
			},
			"status_message": schema.StringAttribute{
				MarkdownDescription: "Human-readable detail accompanying the status.",
				Computed:            true,
			},
		},
	}
}

func (r *controlResetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

// ValidateConfig rejects a mismatched confirmation while the plan is being
// built. Checking it during apply would be too late: by then the apply that
// reaches this resource has already deleted the CloudFormation stack it
// depends on, and refusing at that point leaves the firewall torn down with
// its controls still applied.
func (r *controlResetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config controlResetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	for _, value := range []types.String{config.RootScope, config.Confirm} {
		if value.IsNull() || value.IsUnknown() {
			return
		}
	}

	if config.Confirm.ValueString() != config.RootScope.ValueString() {
		resp.Diagnostics.Append(confirmationMismatch(config.RootScope.ValueString(), config.Confirm.ValueString())...)
	}
}

func (r *controlResetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan controlResetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rootScope := plan.RootScope.ValueString()

	// Checked again here because ValidateConfig only sees values known at plan
	// time, and this is the last point before the organization is cleared.
	if plan.Confirm.ValueString() != rootScope {
		resp.Diagnostics.Append(confirmationMismatch(rootScope, plan.Confirm.ValueString())...)
		return
	}

	tflog.Warn(ctx, "resetting every control", map[string]any{"root_scope": rootScope})

	transactionID, err := r.api.ResetCloudRestrictions(ctx, rootScope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to reset controls", err)...)
		return
	}

	plan.ID = types.StringValue(transactionID)
	plan.TransactionID = types.StringValue(transactionID)

	transaction, waitDiags := r.settle(ctx, transactionID, plan.Wait.ValueBool(), plan.WaitTimeout.ValueString())
	applyResetTransaction(&plan, transaction)

	// State is saved even when waiting failed, so the transaction is recorded
	// rather than lost. Terraform marks the resource tainted when Create
	// returns an error, so the next apply replaces it and resets again.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(waitDiags...)
}

func (r *controlResetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state controlResetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	transaction, err := r.api.ControlTransaction(ctx, state.TransactionID.ValueString())
	if err != nil {
		// A transaction the platform has aged out is kept in state rather than
		// dropped. Dropping it would make the next plan propose creating the
		// resource again, and creating it means resetting the organization a
		// second time.
		if isNotFound(err) {
			tflog.Info(ctx, "reset transaction is no longer readable, keeping the recorded result",
				map[string]any{"transaction_id": state.TransactionID.ValueString()})
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read reset transaction", err)...)
		return
	}

	applyResetTransaction(&state, transaction)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *controlResetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// root_scope and confirm force replacement, so only wait and wait_timeout
	// reach here. Neither describes anything on the server: a reset that has
	// already run is not run again because the timeout changed.
	var plan, state controlResetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	plan.TransactionID = state.TransactionID
	plan.Status = state.Status
	plan.StatusMessage = state.StatusMessage

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *controlResetResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// Removing the record does not undo the reset, and nothing needs undoing:
	// the controls it unapplied are staged, and submitting them deploys them
	// again. This is the normal way out of a reset, so it is not worth a
	// warning on every apply that clears the flag.
	tflog.Info(ctx, "forgetting a completed reset")
}

// settle waits for the reset to complete when asked, and reads it once when
// not.
func (r *controlResetResource) settle(ctx context.Context, transactionID string, wait bool, timeout string) (*client.Transaction, diag.Diagnostics) {
	if !wait {
		transaction, err := r.api.ControlTransaction(ctx, transactionID)
		if err != nil {
			return nil, diagnoseAPIError("Unable to read the reset transaction", err)
		}
		return transaction, nil
	}

	duration := parseDurationOr(timeout, defaultResetWaitTimeout)

	// COMPLETED rather than any terminal status: a reset that settles on
	// FAILED or CANCELED has left the organization half-way through, and
	// reporting that as success would hide it.
	transaction, err := r.api.WaitForTransactionStatus(ctx, transactionID, client.StatusCompleted, duration)
	if err != nil {
		return transaction, diagnoseAPIError("Reset transaction did not complete", err)
	}
	return transaction, nil
}

// confirmationMismatch reports a confirm that does not repeat root_scope.
func confirmationMismatch(rootScope, confirm string) diag.Diagnostics {
	var diags diag.Diagnostics
	diags.AddAttributeError(
		path.Root("confirm"),
		"Confirmation does not match",
		fmt.Sprintf("confirm must repeat root_scope exactly. Expected %q, got %q.", rootScope, confirm),
	)
	return diags
}

func applyResetTransaction(m *controlResetModel, t *client.Transaction) {
	if t == nil {
		m.Status = types.StringValue("")
		m.StatusMessage = types.StringValue("")
		return
	}
	m.Status = types.StringValue(t.Status)
	m.StatusMessage = types.StringValue(t.StatusMsg)
}
