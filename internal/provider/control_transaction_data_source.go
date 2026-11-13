package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ datasource.DataSource              = (*controlTransactionDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*controlTransactionDataSource)(nil)
)

type controlTransactionDataSource struct {
	api *client.Client
}

// NewControlTransactionDataSource returns the sonrai_control_transaction data source.
func NewControlTransactionDataSource() datasource.DataSource {
	return &controlTransactionDataSource{}
}

type controlTransactionModel struct {
	ID               types.String `tfsdk:"id"`
	TransactionID    types.String `tfsdk:"transaction_id"`
	WaitFor          types.String `tfsdk:"wait_for"`
	WaitTimeout      types.String `tfsdk:"wait_timeout"`
	RootScope        types.String `tfsdk:"root_scope"`
	Status           types.String `tfsdk:"status"`
	StatusMessage    types.String `tfsdk:"status_message"`
	TemplateLocation types.String `tfsdk:"template_location"`
}

func (d *controlTransactionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_control_transaction"
}

func (d *controlTransactionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a control transaction, optionally waiting for it to reach a terminal " +
			"status.\n\n" +
			"To confirm a rollout, deploy the template with `aws_cloudformation_stack`, then set " +
			"`wait_for = \"COMPLETED\"` here with `depends_on` referencing the stack.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `transaction_id`.",
				Computed:            true,
			},
			"transaction_id": schema.StringAttribute{
				MarkdownDescription: "Transaction to read, from `sonrai_control_submission.<name>.transaction_id`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"wait_for": schema.StringAttribute{
				MarkdownDescription: "Terminal status to wait for, one of `COMPLETED`, `FAILED` or " +
					"`CANCELLED`. Omit to read the transaction once without waiting. Reaching a " +
					"terminal status other than this one is an error.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.StatusCompleted, client.StatusFailed, client.StatusCancelled, "CANCELED"),
				},
			},
			"wait_timeout": schema.StringAttribute{
				MarkdownDescription: "How long to wait, as a Go duration such as `30m`. Only read when " +
					"`wait_for` is set. Defaults to `" + defaultWaitTimeout + "`, and cannot " +
					"exceed `24h`, the point at which a transaction expires. Reaching `COMPLETED` " +
					"includes the CloudFormation rollout, which can take a couple of hours on a first " +
					"rollout across a very large organization.",
				Optional: true,
				Validators: []validator.String{
					durationValidator{},
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Root scope this transaction belongs to.",
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
			// The platform also signs a short-lived download URL for the
			// template. It is deliberately not exposed: it is a bearer
			// credential that would persist in state long after it expired,
			// and it is only ever useful while the transaction is active.
			"template_location": schema.StringAttribute{
				MarkdownDescription: "CloudFormation template this transaction produced, empty when none was needed.",
				Computed:            true,
			},
		},
	}
}

func (d *controlTransactionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		d.api = api
	}
}

func (d *controlTransactionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config controlTransactionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	transactionID := config.TransactionID.ValueString()

	var (
		transaction *client.Transaction
		err         error
	)
	if config.WaitFor.IsNull() {
		transaction, err = d.api.ControlTransaction(ctx, transactionID)
	} else {
		target := config.WaitFor.ValueString()
		timeout := parseDurationOr(config.WaitTimeout.ValueString(), defaultWaitTimeout)
		tflog.Info(ctx, "waiting for control transaction", map[string]any{
			"transaction_id": transactionID, "wait_for": target, "timeout": timeout.String(),
		})
		transaction, err = d.api.WaitForTransactionStatus(ctx, transactionID, target, timeout)
	}

	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read control transaction", err)...)
		return
	}

	config.ID = types.StringValue(transaction.ID)
	config.RootScope = types.StringValue(transaction.RootScope)
	config.Status = types.StringValue(transaction.Status)
	config.StatusMessage = types.StringValue(transaction.StatusMsg)
	config.TemplateLocation = types.StringValue(transaction.TemplateLocation)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}

// parseDurationOr falls back to fallback when value is empty or unparseable.
// Configured values are validated at plan time, so the fallback covers the
// attribute simply being unset.
func parseDurationOr(value, fallback string) time.Duration {
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration
	}
	duration, _ := time.ParseDuration(fallback)
	return duration
}
