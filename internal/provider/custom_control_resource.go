package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ resource.Resource                = (*customControlResource)(nil)
	_ resource.ResourceWithConfigure   = (*customControlResource)(nil)
	_ resource.ResourceWithImportState = (*customControlResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*customControlResource)(nil)
)

// The platform's own rule for a new control's name, applied to the title
// before it is lower-cased and its spaces removed to make the key.
var customControlTitlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]*$`)

var customControlPermissionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._/*-]*$`)

// maxCustomControlPermissions bounds one control. The platform sets no limit
// of its own; every permission lands in size-limited policies, and a list this
// long is a templating mistake rather than a control.
const maxCustomControlPermissions = 1000

// Each refresh looks for an outstanding redeploy in every root scope the
// control is deployed under. Past this many it stops and says so, rather than
// make a refresh as slow as the organization is wide.
const maxSyncCheckRootScopes = 20

const defaultCustomControlDeleteTimeout = "30m"

type customControlResource struct {
	api *client.Client
}

// NewCustomControlResource returns the sonrai_custom_control resource.
func NewCustomControlResource() resource.Resource { return &customControlResource{} }

type customControlModel struct {
	ID            types.String `tfsdk:"id"`
	Title         types.String `tfsdk:"title"`
	CloudType     types.String `tfsdk:"cloud_type"`
	Description   types.String `tfsdk:"description"`
	Permissions   types.Set    `tfsdk:"permissions"`
	ControlKey    types.String `tfsdk:"control_key"`
	Revision      types.String `tfsdk:"revision"`
	DeleteTimeout types.String `tfsdk:"delete_timeout"`
}

func (r *customControlResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_custom_control"
}

func (r *customControlResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Defines a custom control: a named group of permissions the firewall can " +
			"protect or block, alongside the controls it ships with.\n\n" +
			"A custom control belongs to the whole organization, not to a root scope, and defining one " +
			"takes effect as soon as it is applied. On its own it protects nothing. Attach it to an " +
			"account by giving its `control_key` to `sonrai_account_protected_service` or " +
			"`sonrai_account_disabled_service`; that part is staged and submitted like any other " +
			"control.\n\n" +
			"A control with the same name that already exists -- made in the console, or by another " +
			"configuration -- is rejected rather than adopted; import it instead.\n\n" +
			"~> **Editing the permissions of a control that is deployed needs a redeploy.** The " +
			"definition changes at once, but every account it is deployed in keeps enforcing the old " +
			"list until its root scope is submitted and deployed again. Reference `revision` in the " +
			"`triggers` of each `sonrai_control_submission` and that happens in the same apply. The " +
			"apply warns with the root scopes affected, which can include ones this configuration does " +
			"not manage.\n\n" +
			"~> **A control that is attached cannot be destroyed in the same apply that detaches it.** " +
			"The platform will not delete a control that is still deployed, and detaching one only " +
			"takes effect once that change is submitted and deployed. Remove the attachments and " +
			"apply, then remove this resource and apply again. A plan that would destroy a deployed " +
			"control is rejected, naming where it is deployed, before anything is changed. " +
			"Attachments that were staged and never deployed are discarded with the control.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `cloud_type:control_key`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"title": schema.StringAttribute{
				MarkdownDescription: "Name of the control. Letters, numbers, spaces, hyphens, underscores " +
					"and periods, starting with a letter or number. The platform derives `control_key` " +
					"from it by lower-casing it and removing spaces, so a title that gives a different " +
					"key is a different control and forces replacement. Changing only its case or " +
					"spacing renames the control in place. It cannot match a built-in control.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 128),
					stringvalidator.RegexMatches(customControlTitlePattern,
						"may only contain letters, numbers, spaces, hyphens, underscores and periods, "+
							"and must start with a letter or number"),
				},
			},
			"cloud_type": schema.StringAttribute{
				MarkdownDescription: "Cloud the control is for: `aws` or `gcp`. Forces replacement.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(client.CustomControlCloudTypes...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "What the control is for. Shown wherever the control is listed.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 1024),
				},
			},
			"permissions": schema.SetAttribute{
				MarkdownDescription: "Permissions the control covers, such as `s3:DeleteBucket`. This is " +
					"the complete list: an edit replaces it. Every one must exist for `cloud_type`, " +
					"which the plan checks. For GCP, use the `service.googleapis.com/resource.verb` " +
					fmt.Sprintf("form. At most %d.", maxCustomControlPermissions),
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeBetween(1, maxCustomControlPermissions),
					setvalidator.ValueStringsAre(
						stringvalidator.LengthBetween(1, 256),
						stringvalidator.RegexMatches(customControlPermissionPattern,
							"must be a permission name such as s3:DeleteBucket, with no spaces"),
					),
					uniqueIgnoringCase{},
				},
			},
			"control_key": schema.StringAttribute{
				MarkdownDescription: "Key the platform gave the control. Use it as the `control_key` of " +
					"`sonrai_account_protected_service` or `sonrai_account_disabled_service` to attach " +
					"the control to an account.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"revision": schema.StringAttribute{
				MarkdownDescription: "Opaque value that changes when the control needs redeploying: when " +
					"the permissions of a deployed control are edited, and while such an edit is still " +
					"waiting to be deployed. Reference it in the `triggers` of each " +
					"`sonrai_control_submission` whose root scope uses the control.",
				Computed: true,
			},
			"delete_timeout": schema.StringAttribute{
				MarkdownDescription: "How long destroy waits for a deployment that is already removing " +
					"the control to finish before giving up. Defaults to `" + defaultCustomControlDeleteTimeout + "`, and " +
					"cannot exceed `24h`.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString(defaultCustomControlDeleteTimeout),
				Validators: []validator.String{durationValidator{}},
			},
		},
	}
}

// uniqueIgnoringCase refuses a set holding the same permission twice in
// different case. The platform matches permissions without regard to case, so
// the two are one permission, and the set would never read back as written.
type uniqueIgnoringCase struct{}

func (uniqueIgnoringCase) Description(context.Context) string {
	return "must not repeat a value in different case"
}

func (v uniqueIgnoringCase) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (uniqueIgnoringCase) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, element := range req.ConfigValue.Elements() {
		if element.IsUnknown() {
			return
		}
	}
	var values []string
	resp.Diagnostics.Append(req.ConfigValue.ElementsAs(ctx, &values, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	seen := map[string]string{}
	for _, value := range values {
		folded := strings.ToLower(value)
		if first, ok := seen[folded]; ok {
			resp.Diagnostics.AddAttributeError(req.Path, "Permission listed twice",
				fmt.Sprintf("%q and %q are the same permission; case is not significant.", first, value))
			return
		}
		seen[folded] = value
	}
}

func (r *customControlResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

// ModifyPlan works out the key, decides whether a new title is a new control,
// checks the definition with the platform, and settles revision.
//
// The check is a dry run of the write. Without it an unknown permission or a
// name that collides with a built-in control would surface halfway through an
// apply, after other resources had already staged against the control.
func (r *customControlResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.api == nil {
		return
	}
	if req.Plan.Raw.IsNull() {
		resp.Diagnostics.Append(r.rejectDeployedDestroy(ctx, req)...)
		return
	}
	var plan customControlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Title.IsUnknown() || plan.CloudType.IsUnknown() {
		return
	}
	cloudType := plan.CloudType.ValueString()
	key := client.CustomControlKey(plan.Title.ValueString())

	creating := req.State.Raw.IsNull()
	var state customControlModel
	if !creating {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if state.ControlKey.ValueString() != key {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("title"))
			creating = true
		} else if state.CloudType.ValueString() != cloudType {
			creating = true
		}
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("control_key"), key)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), customControlResourceID(cloudType, key))...)

	definitionKnown := !plan.Description.IsUnknown() && !plan.Permissions.IsUnknown()
	if creating {
		if definitionKnown {
			resp.Diagnostics.Append(r.dryRun(ctx, plan, true)...)
		}
		return
	}

	if plan.Permissions.Equal(state.Permissions) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("revision"), state.Revision)...)
		return
	}
	if definitionKnown {
		resp.Diagnostics.Append(r.dryRun(ctx, plan, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	// A new permission list only needs deploying where the control already
	// is. Anywhere it is merely staged picks the new list up when that is
	// deployed, so with no deployments revision can stay put.
	deployments, err := r.api.CustomControlDeployments(ctx, cloudType, key)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read where the custom control is deployed", err)...)
		return
	}
	if len(deployments) == 0 {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("revision"), state.Revision)...)
	}
}

// rejectDeployedDestroy fails a plan that would destroy a control that is
// still deployed.
//
// Destroy cannot wait for the detach instead. Terraform holds back the
// submission that would deploy it until this resource has been destroyed, so
// the two would wait on each other. Failing at apply would be no better: by
// then the attachments have staged their removal, and the error stops the
// submission that would have deployed it. Refusing the plan leaves everything
// as it was and says what to do first.
func (r *customControlResource) rejectDeployedDestroy(ctx context.Context, req resource.ModifyPlanRequest) diag.Diagnostics {
	var diags diag.Diagnostics
	if req.State.Raw.IsNull() {
		return diags
	}
	var state customControlModel
	diags.Append(req.State.Get(ctx, &state)...)
	if diags.HasError() {
		return diags
	}
	key := state.ControlKey.ValueString()
	deployments, err := r.api.CustomControlDeployments(ctx, state.CloudType.ValueString(), key)
	if err != nil {
		diags.Append(diagnoseAPIError("Unable to read where the custom control is deployed", err)...)
		return diags
	}
	if len(deployments) == 0 {
		return diags
	}
	scopes := make([]string, 0, len(deployments))
	for _, d := range deployments {
		scopes = append(scopes, d.Scope)
	}
	diags.AddError("Custom control is still deployed", stillDeployedDetail(key, scopes))
	return diags
}

func stillDeployedDetail(key string, scopes []string) string {
	return fmt.Sprintf("%q cannot be destroyed while it is deployed, and it is deployed at:\n\n  - %s\n\n"+
		"Detaching it only takes effect once that change has been submitted and deployed, so this takes "+
		"two applies. First remove the control from those scopes, keeping this resource, and apply. Then "+
		"remove this resource and apply again. A scope managed by another configuration, or set in the "+
		"console, has to be detached there. Nothing has been changed.",
		key, strings.Join(scopes, "\n  - "))
}

func (r *customControlResource) dryRun(ctx context.Context, m customControlModel, createOnly bool) diag.Diagnostics {
	control, diags := customControlFromModel(ctx, m)
	if diags.HasError() {
		return diags
	}
	if _, err := r.api.UpsertCustomControl(ctx, control, client.UpsertCustomControlOptions{
		CreateOnly: createOnly,
		DryRun:     true,
	}); err != nil {
		diags.Append(diagnoseCustomControlError("Custom control would be rejected", m, createOnly, err)...)
	}
	return diags
}

func (r *customControlResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan customControlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	control, diags := customControlFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "creating custom control", map[string]any{
		"title": control.Title, "cloud_type": control.CloudType, "permissions": len(control.Permissions),
	})
	// CreateOnly is what rejects a control someone else already defined:
	// taking it over would have destroy delete a control this configuration
	// never made. The same rule as the other resources.
	result, err := r.api.UpsertCustomControl(ctx, control, client.UpsertCustomControlOptions{CreateOnly: true})
	if err != nil {
		resp.Diagnostics.Append(diagnoseCustomControlError("Unable to create custom control", plan, true, err)...)
		return
	}

	plan.ControlKey = types.StringValue(result.Key)
	plan.ID = types.StringValue(customControlResourceID(control.CloudType, result.Key))
	plan.Revision = newRevision()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *customControlResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state customControlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cloudType := state.CloudType.ValueString()
	key := state.ControlKey.ValueString()

	control, err := r.api.CustomControl(ctx, cloudType, key)
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "custom control no longer present, removing from state",
				map[string]any{"control_key": key, "cloud_type": cloudType})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read custom control", err)...)
		return
	}

	var known []string
	if !state.Permissions.IsNull() && !state.Permissions.IsUnknown() {
		resp.Diagnostics.Append(state.Permissions.ElementsAs(ctx, &known, false)...)
	}
	permissions, diags := stringsToSet(ctx, keepSpelling(control.Permissions, known))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.ID = types.StringValue(customControlResourceID(cloudType, key))
	state.Title = types.StringValue(control.Title)
	state.Description = types.StringValue(control.Description)
	state.Permissions = permissions
	ensureRevision(&state.Revision)

	// An edit that was written but never redeployed leaves nothing in the
	// configuration to change, so nothing would ever submit it. Moving
	// revision while the redeploy is outstanding is what brings it back.
	outstanding, err := r.syncOutstanding(ctx, cloudType, key)
	if err != nil {
		resp.Diagnostics.AddWarning("Unable to check whether the custom control needs redeploying", err.Error())
	} else if outstanding {
		state.Revision = newRevision()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *customControlResource) syncOutstanding(ctx context.Context, cloudType, key string) (bool, error) {
	deployments, err := r.api.CustomControlDeployments(ctx, cloudType, key)
	if err != nil {
		return false, err
	}
	roots := map[string]struct{}{}
	for _, d := range deployments {
		roots[d.RootScope] = struct{}{}
	}
	if len(roots) > maxSyncCheckRootScopes {
		return false, fmt.Errorf("deployed under %d root scopes, more than the %d checked on a refresh",
			len(roots), maxSyncCheckRootScopes)
	}
	for root := range roots {
		pending, err := r.api.CustomControlSyncPending(ctx, root, key)
		if err != nil {
			return false, err
		}
		if pending {
			return true, nil
		}
	}
	return false, nil
}

func (r *customControlResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state customControlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	plan.ControlKey = state.ControlKey

	// delete_timeout is the provider's own setting; changing it writes nothing.
	if plan.Title.Equal(state.Title) && plan.Description.Equal(state.Description) &&
		plan.Permissions.Equal(state.Permissions) {
		plan.Revision = state.Revision
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	control, diags := customControlFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Info(ctx, "updating custom control", map[string]any{
		"control_key": state.ControlKey.ValueString(), "permissions": len(control.Permissions),
	})
	result, err := r.api.UpsertCustomControl(ctx, control, client.UpsertCustomControlOptions{})
	if err != nil {
		resp.Diagnostics.Append(diagnoseCustomControlError("Unable to update custom control", plan, false, err)...)
		return
	}
	if result.Key != state.ControlKey.ValueString() {
		resp.Diagnostics.AddError("Custom control changed identity",
			fmt.Sprintf("The update was written to control %q, not %q. The title no longer names the "+
				"control this resource manages; a control %q may now exist that Terraform does not track.",
				result.Key, state.ControlKey.ValueString(), result.Key))
		return
	}

	needsDeploy := len(result.SyncRequired) > 0
	revision, submitsNow := revisionAfterEdit(plan.Revision, state.Revision, needsDeploy)
	plan.Revision = revision
	if needsDeploy {
		resp.Diagnostics.AddWarning(
			"Custom control needs redeploying",
			syncRequiredDetail(state.ControlKey.ValueString(), result.SyncRequired, submitsNow),
		)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func syncRequiredDetail(key string, sync []client.CustomControlSync, submitsNow bool) string {
	lines := make([]string, 0, len(sync))
	for _, s := range sync {
		noun := "scopes"
		if len(s.Scopes) == 1 {
			noun = "scope"
		}
		lines = append(lines, fmt.Sprintf("  - %s (%d %s)", s.RootScope, len(s.Scopes), noun))
	}
	sort.Strings(lines)
	return fmt.Sprintf("The new permission list for %q is saved, but where the control is already deployed "+
		"the old list stays in force until the root scope is submitted and deployed again:\n\n%s\n\n"+
		"%s Any root scope listed here that this configuration does not submit has to be deployed "+
		"separately; the change waits in its Pending Changes.",
		key, strings.Join(lines, "\n"), redeployTiming(submitsNow))
}

func (r *customControlResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state customControlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cloudType := state.CloudType.ValueString()
	key := state.ControlKey.ValueString()

	if _, err := r.api.CustomControl(ctx, cloudType, key); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read custom control", err)...)
		return
	}

	// The plan already refused a control that was deployed. What can be left
	// by now is a removal that was mid-rollout when the plan was made.
	timeout := parseDurationOr(state.DeleteTimeout.ValueString(), defaultCustomControlDeleteTimeout)
	if err := r.api.WaitForCustomControlUndeployed(ctx, cloudType, key, timeout); err != nil {
		var still *client.StillDeployedError
		if errors.As(err, &still) {
			resp.Diagnostics.AddError("Custom control is still deployed", stillDeployedDetail(key, still.Scopes))
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Custom control was not detached in time", err)...)
		return
	}

	tflog.Info(ctx, "deleting custom control", map[string]any{"control_key": key, "cloud_type": cloudType})
	if err := r.api.DeleteCustomControl(ctx, cloudType, key); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to delete custom control", err)...)
	}
}

func (r *customControlResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cloudType, key, ok := strings.Cut(req.ID, ":")
	if !ok || key == "" || !contains(client.CustomControlCloudTypes, cloudType) || strings.ContainsAny(key, " \t\n") {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected %q, for example %q, but got %q.", "cloud_type:control_key", "aws:breakglass-actions", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud_type"), cloudType)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("control_key"), key)...)
	// Set so the default does not read an imported null as a change.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("delete_timeout"), defaultCustomControlDeleteTimeout)...)
}

func customControlResourceID(cloudType, key string) string { return cloudType + ":" + key }

func customControlFromModel(ctx context.Context, m customControlModel) (client.CustomControl, diag.Diagnostics) {
	permissions, diags := setToStrings(ctx, m.Permissions)
	return client.CustomControl{
		Title:       m.Title.ValueString(),
		Description: m.Description.ValueString(),
		CloudType:   m.CloudType.ValueString(),
		Permissions: permissions,
	}, diags
}

// diagnoseCustomControlError points the platform's refusals at the attribute
// that caused them, and adds the way out for a control that already exists.
func diagnoseCustomControlError(summary string, m customControlModel, creating bool, err error) diag.Diagnostics {
	var diags diag.Diagnostics
	message := err.Error()
	switch {
	case strings.Contains(message, "permissions are invalid"):
		diags.AddAttributeError(path.Root("permissions"), summary, message)
	case creating && strings.Contains(message, "a control already exists with key"):
		id := customControlResourceID(m.CloudType.ValueString(), client.CustomControlKey(m.Title.ValueString()))
		diags.AddAttributeError(path.Root("title"), "Custom control already exists",
			fmt.Sprintf("A custom control named %q already exists. It is not adopted, because destroying this "+
				"resource would then delete a control it never made. Import it instead: "+
				"terraform import <address> '%s'", m.Title.ValueString(), id))
	case strings.Contains(message, "built-in control already exists"), strings.Contains(message, "names may only contain"):
		diags.AddAttributeError(path.Root("title"), summary, message)
	default:
		diags.Append(diagnoseAPIError(summary, err)...)
	}
	return diags
}

// keepSpelling returns the platform's permissions with each one spelled the
// way it already is in state, where the two differ only by case. The platform
// reports its own capitalisation whatever was written, and without this a
// permission written in another case would show as a change on every plan.
func keepSpelling(platform, state []string) []string {
	spelled := make(map[string]string, len(state))
	for _, s := range state {
		spelled[strings.ToLower(s)] = s
	}
	out := make([]string, 0, len(platform))
	for _, p := range platform {
		if s, ok := spelled[strings.ToLower(p)]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, p)
	}
	return out
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
