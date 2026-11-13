package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

// defaultWaitTimeout suits a normal submission. A first rollout across a very
// large organization can take a couple of hours, so raise wait_timeout there.
const defaultWaitTimeout = "30m"

var (
	_ resource.Resource                = (*controlSubmissionResource)(nil)
	_ resource.ResourceWithConfigure   = (*controlSubmissionResource)(nil)
	_ resource.ResourceWithImportState = (*controlSubmissionResource)(nil)
)

type controlSubmissionResource struct {
	api *client.Client
}

// NewControlSubmissionResource returns the sonrai_control_submission resource.
func NewControlSubmissionResource() resource.Resource { return &controlSubmissionResource{} }

type controlSubmissionModel struct {
	ID               types.String `tfsdk:"id"`
	RootScope        types.String `tfsdk:"root_scope"`
	Triggers         types.Map    `tfsdk:"triggers"`
	Wait             types.Bool   `tfsdk:"wait"`
	WaitTimeout      types.String `tfsdk:"wait_timeout"`
	TransactionID    types.String `tfsdk:"transaction_id"`
	Status           types.String `tfsdk:"status"`
	StatusMessage    types.String `tfsdk:"status_message"`
	TemplateLocation types.String `tfsdk:"template_location"`
}

func (r *controlSubmissionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_control_submission"
}

func (r *controlSubmissionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Submits every control change staged under a root scope as one transaction.\n\n" +
			"Staging resources such as `sonrai_account_disabled_service` take effect only once submitted. A " +
			"submission covers all staged changes under its root scope, including any staged from the " +
			"console. Use one per root scope, and do not run concurrent applies against the same root " +
			"scope.\n\n" +
			"When the submitted changes require it, the transaction produces a CloudFormation template. " +
			"Deploy it with `aws_cloudformation_stack` using `template_location`, or from the console.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Transaction id of this submission.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Cloud organization root scope whose staged changes to submit, for example `aws/r-abcd`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(scopePattern, "must begin with the cloud type: aws/, gcp/ or azure/"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"triggers": schema.MapAttribute{
				MarkdownDescription: "Arbitrary values that cause a new submission when they change. " +
					"Reference the controls this submission should cover, so that changing one submits it.",
				ElementType: types.StringType,
				Optional:    true,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"wait": schema.BoolAttribute{
				MarkdownDescription: "Whether to wait for the transaction to settle before the apply " +
					"returns. Waiting ends when the transaction reaches a terminal status or produces a " +
					"template, whichever comes first. Defaults to true.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"wait_timeout": schema.StringAttribute{
				MarkdownDescription: "How long to wait, as a Go duration such as `30m`. Defaults to `" +
					defaultWaitTimeout + "`, and cannot exceed `24h`, the point at which a transaction " +
					"expires. A first rollout across a very large organization can take a couple of hours.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(defaultWaitTimeout),
				Validators: []validator.String{
					durationValidator{},
				},
			},
			"transaction_id": schema.StringAttribute{
				MarkdownDescription: "Transaction id returned by the platform.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Transaction status reported by the platform. Only " +
					"`COMPLETED`, `FAILED` and `CANCELED` are terminal; the values seen on the way " +
					"vary with what is being deployed.",
				Computed: true,
			},
			"status_message": schema.StringAttribute{
				MarkdownDescription: "Human-readable detail accompanying the status.",
				Computed:            true,
			},
			// The platform's pre-signed template URL is deliberately absent,
			// here and on the sonrai_control_transaction data source: it is
			// signed fresh on every read (so storing it means perpetual
			// drift), it is a bearer credential that would sit in state, and
			// it expires with the transaction. Deploy via template_location.
			"template_location": schema.StringAttribute{
				MarkdownDescription: "CloudFormation template this transaction produced. Empty when the " +
					"submitted changes do not alter the template, in which case there is nothing to deploy.",
				Computed: true,
			},
		},
	}
}

func (r *controlSubmissionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *controlSubmissionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan controlSubmissionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	rootScope := plan.RootScope.ValueString()
	tflog.Info(ctx, "submitting staged control changes", map[string]any{"root_scope": rootScope})

	transactionID, err := r.api.SubmitControls(ctx, rootScope)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to submit staged changes", err)...)
		return
	}

	plan.ID = types.StringValue(transactionID)
	plan.TransactionID = types.StringValue(transactionID)

	transaction, waitDiags := r.settle(ctx, transactionID, plan.Wait.ValueBool(), plan.WaitTimeout.ValueString())
	applyTransaction(&plan, transaction)

	// State is saved even when waiting failed: the submission happened, and
	// dropping it would leave a live transaction Terraform has no record of.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(waitDiags...)
}

func (r *controlSubmissionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state controlSubmissionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	transaction, err := r.api.ControlTransaction(ctx, state.TransactionID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read control transaction", err)...)
		return
	}

	// A cancelled transaction submitted nothing: the changes it carried return
	// to staged. Keeping the resource would leave a record of a submission that
	// never happened, and since nothing in configuration changed, Terraform
	// would never offer to submit them again. Dropping it means the next plan
	// proposes a fresh submission, which is what the configuration asks for.
	if submissionCancelled(transaction.Status) {
		tflog.Info(ctx, "submission was cancelled, removing from state", map[string]any{
			"transaction_id": state.TransactionID.ValueString(),
		})
		resp.State.RemoveResource(ctx)
		return
	}

	// root_scope is unknown right after an import; the transaction carries it.
	// Only the null case is filled so a configured spelling is never rewritten.
	if state.RootScope.IsNull() && transaction.RootScope != "" {
		state.RootScope = types.StringValue(transaction.RootScope)
	}

	applyTransaction(&state, transaction)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *controlSubmissionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// root_scope and triggers force replacement. Only wait and wait_timeout can
	// reach here, and neither describes anything on the server: a transaction
	// that has already been submitted is not resubmitted because the timeout
	// changed.
	var plan, state controlSubmissionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	plan.TransactionID = state.TransactionID
	plan.Status = state.Status
	plan.StatusMessage = state.StatusMessage
	plan.TemplateLocation = state.TemplateLocation

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *controlSubmissionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state controlSubmissionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no unsubmit. Removing the resource forgets the transaction; it
	// does not undo it. A sonrai_control_reset in the same apply does undo it,
	// which is why this does not claim the changes are still in force.
	resp.Diagnostics.AddWarning(
		"Submitted changes are not reverted by removing this resource",
		"Transaction "+state.TransactionID.ValueString()+" has been removed from Terraform state. "+
			"Removing it does not undo what it submitted: stage and submit the removals, or reset "+
			"the root scope, to do that.",
	)
}

func (r *controlSubmissionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("transaction_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("wait"), true)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("wait_timeout"), defaultWaitTimeout)...)
}

// settle waits for the transaction when asked, and reads it once when not.
func (r *controlSubmissionResource) settle(ctx context.Context, transactionID string, wait bool, timeout string) (*client.Transaction, diag.Diagnostics) {
	if !wait {
		transaction, err := r.api.ControlTransaction(ctx, transactionID)
		if err != nil {
			return nil, diagnoseAPIError("Unable to read the submitted transaction", err)
		}
		return transaction, nil
	}

	duration := parseDurationOr(timeout, defaultWaitTimeout)

	transaction, err := r.api.WaitForTransaction(ctx, transactionID, duration)
	if err != nil {
		return transaction, diagnoseAPIError("Submitted transaction did not complete", err)
	}
	return transaction, nil
}

// submissionCancelled reports whether status means the submission was undone
// and its changes returned to staged, in either spelling the platform uses.
func submissionCancelled(status string) bool {
	return client.NormalizeStatus(status) == client.StatusCancelled
}

func applyTransaction(m *controlSubmissionModel, t *client.Transaction) {
	if t == nil {
		m.Status = types.StringValue("")
		m.StatusMessage = types.StringValue("")
		m.TemplateLocation = types.StringValue("")
		return
	}
	m.Status = types.StringValue(t.Status)
	m.StatusMessage = types.StringValue(t.StatusMsg)
	m.TemplateLocation = types.StringValue(t.TemplateLocation)
}
