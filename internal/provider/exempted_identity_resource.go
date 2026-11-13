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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var (
	_ resource.Resource                = (*exemptedIdentityResource)(nil)
	_ resource.ResourceWithConfigure   = (*exemptedIdentityResource)(nil)
	_ resource.ResourceWithImportState = (*exemptedIdentityResource)(nil)
)

// exemptionTypeNames maps the configuration spelling to the API's.
var exemptionTypeNames = map[string]string{
	"service_protect": client.ExemptServiceProtect,
	"service_block":   client.ExemptServiceBlock,
	"quarantine":      client.ExemptQuarantine,
	"region_block":    client.ExemptRegionBlock,
	"jit":             client.ExemptJIT,
	"tamper_protect":  client.ExemptTamperProtect,
}

type exemptedIdentityResource struct {
	api *client.Client
}

// NewExemptedIdentityResource returns the sonrai_exempted_identity resource.
func NewExemptedIdentityResource() resource.Resource { return &exemptedIdentityResource{} }

type exemptedIdentityModel struct {
	ID         types.String `tfsdk:"id"`
	Scope      types.String `tfsdk:"scope"`
	Identity   types.String `tfsdk:"identity"`
	SSO        types.Bool   `tfsdk:"sso"`
	ExemptFrom types.Set    `tfsdk:"exempt_from"`
	Pending    types.Bool   `tfsdk:"pending"`
}

func (r *exemptedIdentityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exempted_identity"
}

func (r *exemptedIdentityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Exempts an identity from whole classes of firewall control at a scope " +
			"and everything beneath it.\n\n" +
			"Set `scope` to a root scope to exempt across the whole organization, which is what a " +
			"break-glass role usually wants. Set it to an account to exempt only there.\n\n" +
			"This is not the same as the per-control exemptions on " +
			"`sonrai_account_protected_service`. Those name identities exempt from one control; this " +
			"exempts an identity from every control of the listed kinds.\n\n" +
			"An identity already exempted at the scope -- from the console, or by another " +
			"configuration -- is rejected rather than adopted; import it instead.\n\n" +
			"AWS only for now.\n\n" +
			"Identities are matched as **patterns**, so `arn:aws:iam::*:role/BreakGlass` covers that " +
			"role in every account. A pattern that names no resource, such as `arn:aws:iam::`, would " +
			"match every ARN in the organization and is refused.\n\n" +
			"Creating this resource *stages* the change. It takes effect only once the pending " +
			"changes for the containing root scope are submitted and deployed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the form `identity@scope`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"scope": schema.StringAttribute{
				MarkdownDescription: "Scope to define the exemption at: a root scope such as " +
					"`aws/r-abcd` for the whole organization, or a scope path beneath it. A " +
					"management-account scope is rejected when the change is deployed.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(awsScopePattern, "must be an aws/ scope; exempted identities are AWS only for now"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"identity": schema.StringAttribute{
				MarkdownDescription: "ARN pattern to exempt, such as " +
					"`arn:aws:iam::*:role/BreakGlass`, or the SSO actor id when `sso` is true.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					identityPatternValidator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"sso": schema.BoolAttribute{
				MarkdownDescription: "Whether `identity` is an IAM Identity Center user rather than " +
					"an ARN pattern. Defaults to false.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"pending": schema.BoolAttribute{
				MarkdownDescription: "True while this exemption has a staged change that has not " +
					"been submitted and deployed.",
				Computed: true,
			},
			"exempt_from": schema.SetAttribute{
				MarkdownDescription: "Control kinds this identity is exempt from: " +
					"`service_protect`, `service_block`, `quarantine`, `region_block`, `jit`, " +
					"`tamper_protect`. Kinds left out are off.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf(exemptionTypeKeys()...)),
				},
			},
		},
	}
}

func (r *exemptedIdentityResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if api := configureClient(req.ProviderData, &resp.Diagnostics); api != nil {
		r.api = api
	}
}

func (r *exemptedIdentityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan exemptedIdentityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An identity someone already exempted here is rejected rather than
	// adopted: recording it would have destroy remove an exemption this
	// configuration never caused. The same rule as the control resources.
	scope := plan.Scope.ValueString()
	identity := plan.Identity.ValueString()
	if existing, err := r.api.ExemptedIdentity(ctx, scope, identity, plan.SSO.ValueBool()); err == nil &&
		len(existing.Types) > 0 {
		importID := exemptedIdentityID(plan)
		if plan.SSO.ValueBool() {
			importID += importSeparator + "sso"
		}
		resp.Diagnostics.AddAttributeError(
			path.Root("identity"),
			"Identity is already exempted at this scope",
			fmt.Sprintf("%s is already exempt from %s at %s. Import the existing exemption "+
				"instead: terraform import <address> '%s'",
				identity, strings.Join(configNames(existing.Types), ", "), scope, importID),
		)
		return
	}

	resp.Diagnostics.Append(r.stage(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *exemptedIdentityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state exemptedIdentityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope := state.Scope.ValueString()
	identity := state.Identity.ValueString()

	entry, err := r.api.ExemptedIdentity(ctx, scope, identity, state.SSO.ValueBool())
	if err != nil {
		if isNotFound(err) {
			tflog.Info(ctx, "exempted identity no longer present, removing from state",
				map[string]any{"identity": identity, "scope": scope})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read exempted identity", err)...)
		return
	}

	// An entry stripped of every type is gone in all but name.
	if len(entry.Types) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	exemptFrom, diags := stringsToSet(ctx, configNames(entry.Types))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.ExemptFrom = exemptFrom
	state.Pending = types.BoolValue(entry.Pending)
	state.ID = types.StringValue(exemptedIdentityID(state))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *exemptedIdentityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan exemptedIdentityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only exempt_from can reach here; everything else forces replacement. The
	// mutation carries every type on each call, so restaging is the update.
	resp.Diagnostics.Append(r.stage(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *exemptedIdentityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state exemptedIdentityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scope := state.Scope.ValueString()
	identity := state.Identity.ValueString()
	isSSO := state.SSO.ValueBool()

	types_, diags := setToStrings(ctx, state.ExemptFrom)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	entry, err := r.api.ExemptedIdentity(ctx, scope, identity, isSSO)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read exempted identity", err)...)
		return
	}
	if isNotFound(err) {
		return
	}
	if entry.IsCore {
		resp.Diagnostics.AddError(
			"Cannot remove a built-in exemption",
			fmt.Sprintf("%q at %s is one of the platform's own exemptions and cannot be removed. "+
				"Remove the resource from configuration with `terraform state rm` instead.", identity, scope),
		)
		return
	}

	// A deployed exemption has its removal staged; one that exists only as a
	// staged entry must be withdrawn instead, or the removal would be staged
	// alongside it and the next deploy would apply the exemption on its way to
	// undoing it. Which case this is cannot be read off the merged entry, so
	// the applied rows are checked directly.
	deployed, err := r.api.ExemptedIdentityDeployed(ctx, scope, identity, isSSO)
	if err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to read exempted identity", err)...)
		return
	}

	if !deployed {
		if err := r.api.DiscardStagedExemptedIdentity(ctx, scope, identity, isSSO); err != nil {
			resp.Diagnostics.Append(diagnoseAPIError("Unable to withdraw the staged exemption", err)...)
		}
		// Withdrawn before it was ever deployed; nothing was in force, so no
		// staged-not-applied warning applies.
		return
	}

	if err := r.api.RemoveExemptedIdentity(ctx, scope, identity, isSSO, apiNames(types_)); err != nil {
		resp.Diagnostics.Append(diagnoseAPIError("Unable to remove exempted identity", err)...)
		return
	}

	resp.Diagnostics.AddWarning(
		"Removing the exemption is staged, not applied",
		fmt.Sprintf("The exemption for %s at %s has been staged for removal. It remains in force "+
			"until the pending changes for its root scope are submitted and deployed, which a "+
			"sonrai_control_submission covering that scope does later in this same apply.",
			identity, scope),
	)
}

func (r *exemptedIdentityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	identity, scope, sso, err := parseExemptedIdentityImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected %q with %q appended for an SSO identity, for example %q or %q, but got %q.",
				"identity"+importSeparator+"scope",
				importSeparator+"sso",
				"arn:aws:iam::*:role/BreakGlass"+importSeparator+"aws/r-abcd",
				"someone@example.com"+importSeparator+"aws/r-abcd"+importSeparator+"sso",
				req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), identity+importSeparator+scope)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("identity"), identity)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("scope"), scope)...)
	// Always set, so the default does not read an imported null as a change.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("sso"), sso)...)
}

// parseExemptedIdentityImportID splits identity@scope[@sso]. The identity may
// itself contain "@" -- SSO actor ids are often email-shaped -- so the id is
// parsed from the right, where a scope cannot contain "@".
func parseExemptedIdentityImportID(id string) (identity, scope string, sso bool, err error) {
	if suffix := importSeparator + "sso"; strings.HasSuffix(id, suffix) {
		sso = true
		id = strings.TrimSuffix(id, suffix)
	}
	at := strings.LastIndex(id, importSeparator)
	if at <= 0 || at == len(id)-1 {
		return "", "", false, fmt.Errorf("import id %q is not identity%sscope", id, importSeparator)
	}
	identity, scope = id[:at], id[at+1:]
	if !scopePattern.MatchString(scope) {
		return "", "", false, fmt.Errorf("import id %q does not end in a scope path", id)
	}
	return identity, scope, sso, nil
}

func (r *exemptedIdentityResource) stage(ctx context.Context, m *exemptedIdentityModel) diag.Diagnostics {
	var diags diag.Diagnostics

	names, d := setToStrings(ctx, m.ExemptFrom)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}

	scope := m.Scope.ValueString()
	identity := m.Identity.ValueString()
	tflog.Info(ctx, "staging exempted identity", map[string]any{
		"identity": identity, "scope": scope, "types": names,
	})

	if err := r.api.StageExemptedIdentity(ctx, scope, identity, m.SSO.ValueBool(), apiNames(names)); err != nil {
		diags.Append(diagnoseAPIError("Unable to stage exempted identity", err)...)
		return diags
	}
	m.ID = types.StringValue(exemptedIdentityID(*m))

	// Computed attributes have to be known once the apply returns. A staging
	// call that just succeeded is by definition undeployed.
	m.Pending = types.BoolValue(true)
	return diags
}

func exemptedIdentityID(m exemptedIdentityModel) string {
	return m.Identity.ValueString() + importSeparator + m.Scope.ValueString()
}

// apiNames converts configuration spellings to the API's.
func apiNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if api, ok := exemptionTypeNames[name]; ok {
			out = append(out, api)
		}
	}
	return out
}

var exemptionConfigNames = func() map[string]string {
	reverse := make(map[string]string, len(exemptionTypeNames))
	for cfg, api := range exemptionTypeNames {
		reverse[api] = cfg
	}
	return reverse
}()

// configNames converts API spellings back to the configuration's.
func configNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if cfg, ok := exemptionConfigNames[name]; ok {
			out = append(out, cfg)
		}
	}
	sort.Strings(out)
	return out
}

func exemptionTypeKeys() []string {
	out := make([]string, 0, len(exemptionTypeNames))
	for name := range exemptionTypeNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// identityPatternValidator refuses a pattern that would match far more than it
// names. The platform accepts these -- its ARN validation lives in the console
// -- and an exemption is matched as a pattern, so "arn:aws:iam::" silently
// exempts every IAM identity in the organization.
type identityPatternValidator struct{}

func (identityPatternValidator) Description(context.Context) string {
	return "must be an ARN pattern that names a resource"
}

func (v identityPatternValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (identityPatternValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	// sso is not readable from here, so an ARN-shaped SSO id is held to the
	// same rule. An SSO actor id is not an ARN, so nothing legitimate is hit.
	if err := client.ValidateExemptedIdentity(req.ConfigValue.ValueString(), false); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Exemption pattern is too broad", err.Error())
	}
}
