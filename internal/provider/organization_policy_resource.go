package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
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
	_ resource.Resource                = (*organizationPolicyResource)(nil)
	_ resource.ResourceWithConfigure   = (*organizationPolicyResource)(nil)
	_ resource.ResourceWithImportState = (*organizationPolicyResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*organizationPolicyResource)(nil)
)

// Organization policies exist only for AWS organizations.
var awsRootScopePattern = regexp.MustCompile(`^aws/[^/\s]+$`)

var policyIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

// The platform trims a name and otherwise takes it as given. Control
// characters are refused here because the name is shown in consoles and logs.
var policyNamePattern = regexp.MustCompile(`^[^\s\p{Cc}]([^\p{Cc}]*[^\s\p{Cc}])?$`)

// maxPolicyDocumentBytes bounds what is sent for review. AWS caps a policy at
// about a tenth of this; the platform's own per-type limit is applied by the
// review, and this only stops something absurd being posted at all.
const maxPolicyDocumentBytes = 100_000

type organizationPolicyResource struct {
	api *client.Client
}

// NewOrganizationPolicyResource returns the sonrai_organization_policy resource.
func NewOrganizationPolicyResource() resource.Resource { return &organizationPolicyResource{} }

type organizationPolicyModel struct {
	ID          types.String         `tfsdk:"id"`
	RootScope   types.String         `tfsdk:"root_scope"`
	Name        types.String         `tfsdk:"name"`
	PolicyType  types.String         `tfsdk:"policy_type"`
	Description types.String         `tfsdk:"description"`
	Content     jsontypes.Normalized `tfsdk:"content"`
	Revision    types.String         `tfsdk:"revision"`
}

func (r *organizationPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_policy"
}

func (r *organizationPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines an organization policy: a service control policy or resource control " +
			"policy of your own that the firewall deploys alongside its own. AWS only.\n\n" +
			"Sonrai does not create a separate policy in AWS. When the policy is deployed its statements " +
			"are packed into the policies Sonrai already manages at each scope, so it uses none of the " +
			"scope's policy attachment slots.\n\n" +
			"Defining a policy takes effect in Sonrai as soon as it is applied, and on its own it is in " +
			"force nowhere. Attach it with `sonrai_organization_policy_attachment`; that part is staged " +
			"and submitted like any other control.\n\n" +
			"The platform does not keep policy names unique. A managed policy that already has this name " +
			"-- made in the console, or by another configuration -- is rejected rather than adopted; " +
			"import it instead.\n\n" +
			"~> **Editing a policy that is deployed needs a redeploy.** The definition changes at once, " +
			"but everywhere it is deployed keeps enforcing the old document until its root scope is " +
			"submitted and deployed again. Reference `revision` in the `triggers` of each " +
			"`sonrai_control_submission` and that happens in the same apply. The apply warns with the " +
			"root scopes affected, which can include ones this configuration does not manage. Until it " +
			"is deployed, the edit can be reverted from Pending Changes in the console, which the next " +
			"plan reports as drift.\n\n" +
			"~> **A policy that is attached cannot be destroyed in the same apply that detaches it.** " +
			"The platform will not delete a policy that is still deployed, and detaching one only " +
			"takes effect once that change is submitted and deployed. Remove the attachments and " +
			"apply, then remove this resource and apply again. A plan that would destroy or replace a " +
			"deployed policy is rejected, naming where it is deployed, before anything is changed. " +
			"Attachments that were staged and never deployed are discarded with the policy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier the platform gave the policy. Give it to " +
					"`sonrai_organization_policy_attachment` as `policy_id`.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"root_scope": schema.StringAttribute{
				MarkdownDescription: "Root scope of the AWS organization the policy is written for, for " +
					"example `aws/r-abcd`. Forces replacement. The platform does not report it back, so " +
					"it is taken from the configuration after an import.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(1024),
					stringvalidator.RegexMatches(awsRootScopePattern,
						"must be an AWS root scope such as aws/r-abcd; organization policies are AWS only"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIf(replaceUnlessImported,
						"Forces replacement, except straight after an import.",
						"Forces replacement, except straight after an import."),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Name of the policy. It can be changed in place. It cannot start with " +
					"`" + client.ReservedPolicyNamePrefix + "`, which the platform keeps for its own policies, " +
					"and cannot begin or end with a space.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 128),
					stringvalidator.RegexMatches(policyNamePattern,
						"must not begin or end with whitespace, or contain control characters"),
					notReservedPolicyName{},
				},
			},
			"policy_type": schema.StringAttribute{
				MarkdownDescription: "`" + client.ServiceControlPolicy + "` or `" + client.ResourceControlPolicy +
					"`. Forces replacement. A resource control policy can only be deployed where the AWS " +
					"organization has that policy type enabled.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.OrganizationPolicyTypes...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "What the policy is for. Once set it can be changed but not removed: " +
					"the platform reads an empty description as \"leave it as it is\".",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 512),
				},
			},
			"content": schema.StringAttribute{
				MarkdownDescription: "The policy document, as JSON. Use `jsonencode` or " +
					"`data.aws_iam_policy_document`. This is the whole document: an edit replaces it. " +
					"Formatting and key order are not significant. The plan has the platform check it, " +
					"and fails with what it finds.",
				Required:   true,
				CustomType: jsontypes.NormalizedType{},
				Validators: []validator.String{
					stringvalidator.LengthBetween(2, maxPolicyDocumentBytes),
					policyDocumentShape{},
				},
			},
			"revision": schema.StringAttribute{
				MarkdownDescription: "Opaque value that changes when the policy needs redeploying: when a " +
					"deployed policy is edited, and while such an edit is still waiting to be deployed. " +
					"Reference it in the `triggers` of each `sonrai_control_submission` whose root scope " +
					"the policy is attached under.",
				Computed: true,
			},
		},
	}
}

// replaceUnlessImported keeps a policy that was just imported, whose root
// scope the platform cannot report, from being replaced by the first plan.
func replaceUnlessImported(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull() && req.StateValue.ValueString() != ""
}

type notReservedPolicyName struct{}

func (notReservedPolicyName) Description(context.Context) string {
	return fmt.Sprintf("must not start with %q", client.ReservedPolicyNamePrefix)
}

func (v notReservedPolicyName) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (notReservedPolicyName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if strings.HasPrefix(req.ConfigValue.ValueString(), client.ReservedPolicyNamePrefix) {
		resp.Diagnostics.AddAttributeError(req.Path, "Reserved policy name",
			fmt.Sprintf("A policy name cannot start with %q; the platform keeps that prefix for the "+
				"policies it deploys itself.", client.ReservedPolicyNamePrefix))
	}
}

// policyDocumentShape catches a document that is not a policy at all before
// it is sent anywhere. What the statements say is left to the platform.
type policyDocumentShape struct{}

func (policyDocumentShape) Description(context.Context) string {
	return "must be a JSON policy document with at least one statement"
}

func (v policyDocumentShape) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (policyDocumentShape) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := policyDocumentProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a policy document", problem)
	}
}

func policyDocumentProblem(content string) string {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		return "The content must be a JSON object: " + err.Error()
	}
	raw, ok := document["Statement"]
	if !ok {
		return `The document has no "Statement".`
	}
	var statements []json.RawMessage
	if err := json.Unmarshal(raw, &statements); err != nil {
		var single map[string]json.RawMessage
		if err := json.Unmarshal(raw, &single); err != nil {
			return `"Statement" must be a statement or a list of statements.`
		}
		return ""
	}
	if len(statements) == 0 {
		return "The document needs at least one statement."
	}
	return ""
}

func (r *organizationPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

// ModifyPlan checks the document and the name with the platform, settles
// revision, and refuses to destroy a policy that is still deployed.
//
// The platform answers a document it will not take with an error that carries
// no reason, and never refuses a duplicate name. Both are therefore checked
// here, where the plan can still say what is wrong and nothing has been
// written.
func (r *organizationPolicyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.api == nil {
		return
	}
	if req.Plan.Raw.IsNull() {
		resp.Diagnostics.Append(r.rejectDeployedDestroy(ctx, req)...)
		return
	}
	var plan organizationPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	creating := req.State.Raw.IsNull()
	var state organizationPolicyModel
	if !creating {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !state.Description.IsNull() && state.Description.ValueString() != "" && plan.Description.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("description"), "Description cannot be removed",
				"The platform reads an empty description as \"leave it as it is\", so removing it would "+
					"change nothing and show as a difference on every plan. Set it to something else instead.")
			return
		}
	}
	replacing := !creating && (!plan.PolicyType.Equal(state.PolicyType) ||
		(!state.RootScope.IsNull() && state.RootScope.ValueString() != "" && !plan.RootScope.Equal(state.RootScope)))

	if replacing {
		resp.Diagnostics.Append(r.rejectDeployedDestroy(ctx, req)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	contentChanged := creating || replacing || !sameDocument(plan.Content, state.Content)
	nameChanged := creating || replacing || !plan.Name.Equal(state.Name)

	if contentChanged && !plan.Content.IsUnknown() && !plan.PolicyType.IsUnknown() && !plan.RootScope.IsUnknown() {
		resp.Diagnostics.Append(r.review(ctx, plan)...)
	}
	if nameChanged && !plan.Name.IsUnknown() && !plan.RootScope.IsUnknown() {
		resp.Diagnostics.Append(r.rejectTakenName(ctx, plan, state.ID.ValueString())...)
	}
	if resp.Diagnostics.HasError() || creating || replacing {
		return
	}

	if !contentChanged && !nameChanged && plan.Description.Equal(state.Description) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("revision"), state.Revision)...)
		return
	}
	// An edit only needs deploying where the policy already is. Anywhere it is
	// merely staged picks the new document up when that is deployed.
	detail, err := r.api.OrganizationPolicy(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read where the organization policy is deployed", err)...)
		return
	}
	if !detail.Deployed() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("revision"), state.Revision)...)
	}
}

func sameDocument(a, b jsontypes.Normalized) bool {
	if a.IsNull() || a.IsUnknown() || b.IsNull() || b.IsUnknown() {
		return a.Equal(b)
	}
	equal, diags := a.StringSemanticEquals(context.Background(), b)
	return equal && !diags.HasError()
}

func (r *organizationPolicyResource) review(ctx context.Context, plan organizationPolicyModel) diag.Diagnostics {
	var diags diag.Diagnostics
	review, err := r.api.ReviewOrganizationPolicy(ctx, plan.PolicyType.ValueString(), plan.Content.ValueString(), plan.RootScope.ValueString())
	if err != nil {
		diags.Append(diagnoseOrganizationPolicyError("Unable to check the policy document", err)...)
		return diags
	}
	if review.SizeLimit > 0 && review.Size > review.SizeLimit {
		diags.AddAttributeError(path.Root("content"), "Policy document is too large",
			fmt.Sprintf("The document is %d characters without whitespace; a %s can hold %d.",
				review.Size, plan.PolicyType.ValueString(), review.SizeLimit))
	}
	for _, finding := range review.Findings {
		detail := finding.Details
		if finding.Code != "" {
			detail = fmt.Sprintf("%s (%s)", detail, finding.Code)
		}
		if finding.Blocking() {
			diags.AddAttributeError(path.Root("content"), "Policy document would be rejected", detail)
			continue
		}
		diags.AddAttributeWarning(path.Root("content"), "Policy document finding", detail)
	}
	return diags
}

// rejectTakenName refuses a name another managed policy already has. The
// platform would accept it and leave two policies no one can tell apart, one
// of them outside this configuration.
func (r *organizationPolicyResource) rejectTakenName(ctx context.Context, plan organizationPolicyModel, ownID string) diag.Diagnostics {
	var diags diag.Diagnostics
	name := plan.Name.ValueString()
	named, err := r.api.ManagedOrganizationPoliciesNamed(ctx, plan.RootScope.ValueString(), name)
	if err != nil {
		diags.Append(diagnoseOrganizationPolicyError("Unable to check the policy name", err)...)
		return diags
	}
	for _, other := range named {
		if other.ID == ownID {
			continue
		}
		diags.AddAttributeError(path.Root("name"), "Organization policy already exists",
			fmt.Sprintf("A managed organization policy named %q already exists. It is not adopted, because "+
				"destroying this resource would then remove a policy it never made. Import it instead: "+
				"terraform import <address> '%s'", name, other.ID))
		return diags
	}
	return diags
}

// rejectDeployedDestroy fails a plan that would destroy, or replace, a policy
// that is still deployed.
//
// Destroy cannot wait for the detach instead: Terraform holds back the
// submission that would deploy it until this resource has been destroyed, so
// the two would wait on each other. The platform's own delete of a deployed
// policy does not help either. It stages the removal and promises to delete
// the policy once that is deployed, but the policy is left behind, detached
// and out of Terraform's sight, still holding its name. Failing at apply would
// strand the detachments already staged. Refusing the plan leaves everything
// as it was and says what to do first.
func (r *organizationPolicyResource) rejectDeployedDestroy(ctx context.Context, req resource.ModifyPlanRequest) diag.Diagnostics {
	var diags diag.Diagnostics
	if req.State.Raw.IsNull() {
		return diags
	}
	var state organizationPolicyModel
	diags.Append(req.State.Get(ctx, &state)...)
	if diags.HasError() {
		return diags
	}
	detail, err := r.api.OrganizationPolicy(ctx, state.ID.ValueString())
	if err != nil {
		if !isNotFound(err) {
			diags.Append(diagnoseOrganizationPolicyError("Unable to read where the organization policy is deployed", err)...)
		}
		return diags
	}
	if detail.Deployed() {
		diags.AddError("Organization policy is still deployed",
			policyStillDeployedDetail(state.Name.ValueString(), detail.DeployedScopes()))
	}
	return diags
}

func policyStillDeployedDetail(name string, scopes []string) string {
	return fmt.Sprintf("%q cannot be destroyed or replaced while it is deployed, and it is deployed at:\n\n  - %s\n\n"+
		"Detaching it only takes effect once that change has been submitted and deployed, so this takes "+
		"two applies. First remove its attachments, keeping this resource as it is, and apply. Then "+
		"remove or replace this resource and apply again. An attachment made by another configuration, "+
		"or in the console, has to be detached there. Nothing has been changed.",
		name, strings.Join(scopes, "\n  - "))
}

func (r *organizationPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Looked up again at apply: the name was free when the plan was made, and
	// the platform itself will not object if it no longer is.
	resp.Diagnostics.Append(r.rejectTakenName(ctx, plan, "")...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "creating organization policy", map[string]any{
		"name": plan.Name.ValueString(), "policy_type": plan.PolicyType.ValueString(),
	})
	created, err := r.api.CreateOrganizationPolicy(ctx, policyFromModel(plan))
	if err != nil {
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to create organization policy", err)...)
		return
	}
	plan.ID = types.StringValue(created.ID)
	plan.Revision = newRevision()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *organizationPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	detail, err := r.api.OrganizationPolicy(ctx, id)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "organization policy no longer present, removing from state", map[string]any{"id": id})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to read organization policy", err)...)
		return
	}
	policy := detail.Policy
	if !policy.Authored {
		resp.Diagnostics.AddError("Not an authored organization policy",
			fmt.Sprintf("%q (%s) is a policy Sonrai discovered in AWS and took over, not one written in "+
				"Sonrai. This resource only manages authored policies: destroying a discovered one would "+
				"hand it back to AWS rather than remove it.", policy.Name, id))
		return
	}

	state.Name = types.StringValue(policy.Name)
	state.PolicyType = types.StringValue(policy.Type)
	state.Content = jsontypes.NewNormalizedValue(policy.Content)
	if policy.Description == "" {
		state.Description = types.StringNull()
	} else {
		state.Description = types.StringValue(policy.Description)
	}
	ensureRevision(&state.Revision)

	// An edit that was written but never redeployed leaves nothing in the
	// configuration to change, so nothing would ever submit it. Moving
	// revision while the redeploy is outstanding is what brings it back.
	outstanding, err := r.syncOutstanding(ctx, detail)
	if err != nil {
		resp.Diagnostics.AddWarning("Unable to check whether the organization policy needs redeploying", err.Error())
	} else if outstanding {
		state.Revision = newRevision()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *organizationPolicyResource) syncOutstanding(ctx context.Context, detail *client.OrganizationPolicyDetail) (bool, error) {
	roots := detail.DeployedRootScopes()
	if len(roots) > maxSyncCheckRootScopes {
		return false, fmt.Errorf("deployed under %d root scopes, more than the %d checked on a refresh",
			len(roots), maxSyncCheckRootScopes)
	}
	for _, root := range roots {
		pending, err := r.api.OrganizationPolicySyncPending(ctx, root, detail.Policy.ID)
		if err != nil {
			return false, err
		}
		if pending {
			return true, nil
		}
	}
	return false, nil
}

func (r *organizationPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state organizationPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	id := state.ID.ValueString()

	// Only root_scope can differ here, and only after an import, where it is
	// recorded from the configuration and nothing is written.
	if plan.Name.Equal(state.Name) && plan.Description.Equal(state.Description) && sameDocument(plan.Content, state.Content) {
		plan.Revision = state.Revision
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if !plan.Name.Equal(state.Name) {
		resp.Diagnostics.Append(r.rejectTakenName(ctx, plan, id)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	tflog.Info(ctx, "updating organization policy", map[string]any{"id": id})
	if err := r.api.UpdateOrganizationPolicy(ctx, id, policyFromModel(plan)); err != nil {
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to update organization policy", err)...)
		return
	}

	detail, err := r.api.OrganizationPolicy(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to read organization policy after updating it", err)...)
		return
	}
	needsDeploy := detail.Deployed()
	switch {
	case needsDeploy:
		// Set even when the plan pinned it: the policy may have been deployed
		// between the plan and now, and a pinned revision would leave the edit
		// undeployed with nothing to submit it.
		plan.Revision = newRevision()
	case plan.Revision.IsUnknown():
		plan.Revision = state.Revision
	}
	if needsDeploy {
		resp.Diagnostics.AddWarning("Organization policy needs redeploying",
			policyRedeployDetail(plan.Name.ValueString(), detail.DeployedRootScopes()))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func policyRedeployDetail(name string, rootScopes []string) string {
	return fmt.Sprintf("The edit to %q is saved, but where the policy is already deployed the old version "+
		"stays in force until the root scope is submitted and deployed again:\n\n  - %s\n\n"+
		"A sonrai_control_submission whose triggers reference this resource's revision does that in this "+
		"apply. Any root scope listed here that this configuration does not submit has to be deployed "+
		"separately; the change waits in its Pending Changes.", name, strings.Join(rootScopes, "\n  - "))
}

func (r *organizationPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	name := state.Name.ValueString()

	detail, err := r.api.OrganizationPolicy(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to read organization policy", err)...)
		return
	}
	if !detail.Policy.Authored {
		resp.Diagnostics.AddError("Not an authored organization policy",
			fmt.Sprintf("%q (%s) was discovered in AWS, not written in Sonrai. It is left alone.", name, id))
		return
	}

	// The plan already refused a policy that was deployed. This catches one
	// that was attached and deployed between the plan and now.
	if detail.Deployed() {
		resp.Diagnostics.AddError("Organization policy is still deployed",
			policyStillDeployedDetail(name, detail.DeployedScopes()))
		return
	}

	tflog.Info(ctx, "deleting organization policy", map[string]any{"id": id})
	if err := r.api.UnmanageOrganizationPolicy(ctx, id); err != nil {
		var live *client.PolicyStillLiveError
		if errors.As(err, &live) {
			resp.Diagnostics.AddError("Organization policy is being deployed",
				fmt.Sprintf("%q cannot be deleted right now: %s. Apply again once the deployment has "+
					"finished.", name, live.Error()))
			return
		}
		resp.Diagnostics.Append(diagnoseOrganizationPolicyError("Unable to delete organization policy", err)...)
	}
}

func (r *organizationPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !policyIDPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected the policy's id, for example %q, but got %q. The "+
				"sonrai_organization_policies data source lists them.",
				"a1b2c3d4-0000-4000-8000-000000000000", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(req.ID))...)
}

func policyFromModel(m organizationPolicyModel) client.NewOrganizationPolicy {
	return client.NewOrganizationPolicy{
		Name:        m.Name.ValueString(),
		Description: m.Description.ValueString(),
		Type:        m.PolicyType.ValueString(),
		Content:     m.Content.ValueString(),
		Scope:       m.RootScope.ValueString(),
	}
}

// diagnoseOrganizationPolicyError adds what the platform's own errors leave
// out: that the feature can be switched off for an organization, and that a
// refused write comes back with no reason attached.
func diagnoseOrganizationPolicyError(summary string, err error) diag.Diagnostics {
	message := err.Error()
	lower := strings.ToLower(message)
	var diags diag.Diagnostics
	switch {
	case errors.Is(err, client.ErrUnauthorized):
		return diagnoseAPIError(summary, err)
	case strings.Contains(lower, "unimplemented"):
		diags.AddError("Organization policies are not enabled",
			"The platform reports that organization policies are switched off for this organization. "+
				"Ask Sonrai to enable them.\n\nUnderlying error: "+message)
	case strings.Contains(lower, "permissiondenied"), strings.Contains(lower, "permission_denied"),
		strings.Contains(lower, "outside the scopes you have access to"):
		diags.AddError(summary, "The token's identity is not permitted to change this policy. Changing an "+
			"organization policy changes it everywhere it is attached, so the identity needs every one of "+
			"those scopes.\n\nUnderlying error: "+message)
	case strings.Contains(lower, "unknown or internal grpc error"):
		diags.AddError(summary, "The platform refused the request without giving a reason. It does this "+
			"for a policy document or name it will not accept, and for a change that conflicts with one "+
			"that is staged.\n\nUnderlying error: "+message)
	default:
		diags.AddError(summary, message)
	}
	return diags
}
