package provider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

var scopePattern = regexp.MustCompile(`^(aws|gcp|azure)/`)

// resolveScope turns the model's root_scope and resource_id into the canonical
// scope path, and records it on the model.
//
// Resolving on every operation rather than trusting the stored path is what
// makes a reorganization invisible: when an account or OU is reparented the
// platform rewrites the control's scope, and this picks up the new one instead
// of acting on a path that no longer exists.
func (r *accountDisabledServiceResource) resolveScope(ctx context.Context, m *accountDisabledServiceModel) (string, error) {
	return resolveModelScope(ctx, r.api, m.RootScope, m.ResourceID, &m.Scope)
}

func (r *accountProtectedServiceResource) resolveScope(ctx context.Context, m *accountProtectedServiceModel) (string, error) {
	return resolveModelScope(ctx, r.api, m.RootScope, m.ResourceID, &m.Scope)
}

func resolveModelScope(ctx context.Context, api *client.Client, rootScope, resourceID types.String, scope *types.String) (string, error) {
	resolved, err := resolveAccountScope(ctx, api, rootScope.ValueString(), resourceID.ValueString())
	if err != nil {
		return "", err
	}
	*scope = types.StringValue(resolved)
	return resolved, nil
}

// resolveRejection is a resource id that resolved to something account
// controls cannot target. It is the caller's input being wrong, not the
// object being gone, so it must not read as not-found.
type resolveRejection struct {
	summary string
	detail  string
}

func (e *resolveRejection) Error() string { return e.summary + ": " + e.detail }

// resolveAccountScope turns a root scope and resource id into the canonical
// scope path of an account, rejecting containers. A missing id wraps
// client.ErrNotFound; an id that names the wrong kind of thing returns a
// *resolveRejection.
func resolveAccountScope(ctx context.Context, api *client.Client, rootScope, resourceID string) (string, error) {
	entry, err := api.ResolveScope(ctx, rootScope, resourceID, "")
	if err != nil {
		return "", err
	}

	// Containers are rejected rather than resolved. Staging a control at an OU
	// or root does not mark that scope: the platform expands it to the leaf
	// accounts present at the time, so the control silently covers a set that
	// neither matches the configuration nor tracks accounts added later.
	if !client.IsLeafAccountType(entry.EntryType) {
		return "", &resolveRejection{
			summary: "Not an account",
			detail: fmt.Sprintf("resource_id %q is not an account: it resolves to entry type %q at %s. "+
				"Only accounts, projects and subscriptions are supported. List the member "+
				"accounts individually.",
				resourceID, entry.EntryType, entry.Scope),
		}
	}

	// An account can sit in the hierarchy without being enrolled in the
	// firewall. Control queries against one fail with "invalid scope", which
	// says nothing about why.
	if !entry.PurpleEnabled {
		return "", &resolveRejection{
			summary: "Account is not enabled for Cloud Permissions Firewall",
			detail: fmt.Sprintf("resource_id %q resolves to %s, which is not enrolled in the firewall. "+
				"Onboard the account before managing its controls.", resourceID, entry.Scope),
		}
	}

	return entry.Scope, nil
}

// diagnoseResolveError turns a resolveAccountScope failure into diagnostics.
// Not-found is included; callers that treat it as "drop from state" or
// "already deleted" must branch on isNotFound before coming here.
func diagnoseResolveError(err error) diag.Diagnostics {
	var diags diag.Diagnostics
	var rejection *resolveRejection
	switch {
	case errors.As(err, &rejection):
		diags.AddAttributeError(tfpath.Root("resource_id"), rejection.summary, rejection.detail)
	case errors.Is(err, client.ErrNotFound):
		diags.AddAttributeError(tfpath.Root("resource_id"), "Account not found", err.Error())
	default:
		diags.Append(diagnoseAPIError("Unable to resolve scope", err)...)
	}
	return diags
}

// setToStrings reads a types.Set of strings into a sorted slice.
func setToStrings(ctx context.Context, set types.Set) ([]string, diag.Diagnostics) {
	var out []string
	diags := set.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, diags
}

// emptyStringSet is the default for an unset exemption list, so an omitted
// attribute means "exempt nobody" rather than an unknown value.
func emptyStringSet() types.Set {
	return types.SetValueMust(types.StringType, []attr.Value{})
}

// stringsToSet builds a types.Set of strings, used for computed read-back.
//
// A nil slice becomes the empty set, not a null one: "the account has nothing"
// is a real answer from every read that lands here, and a null set never
// converges with a configured or defaulted [].
func stringsToSet(ctx context.Context, values []string) (types.Set, diag.Diagnostics) {
	if values == nil {
		values = []string{}
	}
	return types.SetValueFrom(ctx, types.StringType, values)
}

// readInto resolves the target's current scope and refreshes the computed
// status fields of m from the API. gone reports that the scope or the service
// no longer exists, so neither does the control.
func (r *accountDisabledServiceResource) readInto(ctx context.Context, m *accountDisabledServiceModel) (gone bool, diags diag.Diagnostics) {
	scope, err := r.resolveScope(ctx, m)
	if err != nil {
		if isNotFound(err) {
			return true, diags
		}
		return false, diagnoseResolveError(err)
	}

	status, err := r.api.ServiceControlStatus(ctx, m.ControlKey.ValueString(), scope)
	if err != nil {
		if isNotFound(err) {
			return true, diags
		}
		return false, diagnoseAPIError("Unable to read service control status", err)
	}

	m.Status = types.StringValue(status.Status)
	m.Pending = types.BoolValue(status.Pending)
	return false, diags
}

// configureClient extracts the shared API client that Configure receives.
// A nil return with no diagnostics means the provider is not configured yet,
// which the framework calls Configure with during validation.
func configureClient(providerData any, diags *diag.Diagnostics) *client.Client {
	if providerData == nil {
		return nil
	}
	api, ok := providerData.(*client.Client)
	if !ok {
		diags.AddError(
			"Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", providerData),
		)
		return nil
	}
	return api
}

// diagnoseAPIError turns a client error into a diagnostic, separating a bad
// token from a failed call so the message points at the actual fix.
func diagnoseAPIError(summary string, err error) diag.Diagnostics {
	var diags diag.Diagnostics
	if errors.Is(err, client.ErrUnauthorized) {
		diags.AddError(
			"Sonrai API rejected the token",
			"The API returned an authentication failure. Check that SONRAI_TOKEN is current and that its "+
				"App identity is permitted on this scope.\n\nUnderlying error: "+err.Error(),
		)
		return diags
	}
	diags.AddError(summary, err.Error())
	return diags
}

// controlID builds the id and import form control_key@root_scope@resource_id.
func controlID(controlKey, rootScope, resourceID types.String) string {
	return controlKey.ValueString() + importSeparator +
		rootScope.ValueString() + importSeparator +
		resourceID.ValueString()
}

// importControlID splits a control_key@root_scope@resource_id import id into
// the id, control_key, root_scope and resource_id attributes.
func importControlID(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, example string) {
	parts := strings.Split(req.ID, importSeparator)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected %q, for example %q, but got %q.",
				"control_key"+importSeparator+"root_scope"+importSeparator+"resource_id",
				example, req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, tfpath.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, tfpath.Root("control_key"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, tfpath.Root("root_scope"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, tfpath.Root("resource_id"), parts[2])...)
}

// isNotFound reports an API error meaning the object is gone, so Read can drop
// the resource from state rather than failing the run.
func isNotFound(err error) bool { return errors.Is(err, client.ErrNotFound) }

// maxWaitTimeout is the point at which a control transaction expires. Waiting
// longer cannot produce a result.
const maxWaitTimeout = 24 * time.Hour

// durationValidator accepts a positive duration no longer than a transaction
// can live.
type durationValidator struct{}

func (durationValidator) Description(context.Context) string {
	return "must be a positive duration of at most 24h, such as 30m or 2h"
}

func (v durationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v durationValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	duration, err := time.ParseDuration(value)
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration",
			fmt.Sprintf("%q is not a duration such as 15m or 1h30m.", value))
		return
	}
	if duration <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration",
			fmt.Sprintf("%q must be greater than zero.", value))
		return
	}
	if duration > maxWaitTimeout {
		resp.Diagnostics.AddAttributeError(req.Path, "Duration too long",
			fmt.Sprintf("%q exceeds 24h, after which a control transaction expires and cannot "+
				"complete.", value))
	}
}

// wasApplied reports whether controlKey had a control in force at scope, as
// opposed to one that was only ever staged.
//
// Removing a staged-only control deletes the staged mapping and nothing was
// ever in effect; removing an applied one stages a removal that holds until it
// is deployed. Only the second case warrants warning the operator. When the
// check itself fails the answer is true, since over-informing is safer than
// implying a live control went away.
func wasApplied(ctx context.Context, api *client.Client, scope string, controlKeys ...string) (bool, []string) {
	applied, err := api.AppliedControlKeys(ctx, scope)
	if err != nil {
		return true, controlKeys
	}
	inForce := make(map[string]struct{}, len(applied))
	for _, key := range applied {
		inForce[key] = struct{}{}
	}
	var matched []string
	for _, key := range controlKeys {
		if _, ok := inForce[key]; ok {
			matched = append(matched, key)
		}
	}
	return len(matched) > 0, matched
}

// jitEntitlementAttrTypes mirrors the nested object on
// sonrai_jit_configuration.enrolled_entitlements.
var jitEntitlementAttrTypes = map[string]attr.Type{
	"name":     types.StringType,
	"type":     types.StringType,
	"auto_pod": types.BoolType,
}

func entitlementsFromSet(ctx context.Context, set types.Set) ([]client.JitEntitlement, diag.Diagnostics) {
	var diags diag.Diagnostics
	if set.IsNull() || set.IsUnknown() {
		return nil, diags
	}
	var models []jitEntitlementModel
	diags.Append(set.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil, diags
	}
	out := make([]client.JitEntitlement, 0, len(models))
	for _, m := range models {
		entitlementType := m.Type.ValueString()
		if entitlementType == "" {
			entitlementType = client.EntitlementPermissionSet
		}
		out = append(out, client.JitEntitlement{
			Name:       m.Name.ValueString(),
			Type:       entitlementType,
			FullAccess: m.AutoPod.ValueBool(),
		})
	}
	return out, diags
}

func entitlementsToSet(ctx context.Context, entitlements []client.JitEntitlement) (types.Set, diag.Diagnostics) {
	objType := types.ObjectType{AttrTypes: jitEntitlementAttrTypes}
	values := make([]attr.Value, 0, len(entitlements))
	var diags diag.Diagnostics
	for _, e := range entitlements {
		obj, d := types.ObjectValue(jitEntitlementAttrTypes, map[string]attr.Value{
			"name":     types.StringValue(e.Name),
			"type":     types.StringValue(e.Type),
			"auto_pod": types.BoolValue(e.FullAccess),
		})
		diags.Append(d...)
		values = append(values, obj)
	}
	if diags.HasError() {
		return types.SetNull(objType), diags
	}
	set, d := types.SetValue(objType, values)
	diags.Append(d...)
	return set, diags
}

// difference returns the members of a that are not in b, sorted.
func difference(a, b []string) []string {
	inB := make(map[string]struct{}, len(b))
	for _, v := range b {
		inB[v] = struct{}{}
	}
	var out []string
	for _, v := range a {
		if _, ok := inB[v]; !ok {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// revisionAttribute is the value a sonrai_control_submission's triggers should
// reference on every resource that stages changes.
//
// It deliberately carries no UseStateForUnknown. The framework marks a computed
// attribute unknown in any plan that changes the resource, and that unknown is
// what makes the submission replace itself -- including when the plan only
// reverts drift, where every configured value is the same as last time and a
// trigger built from configuration would not move.
func revisionAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "Opaque value that changes every time this resource stages a change. " +
			"Reference it in the `triggers` of the `sonrai_control_submission` for its root scope, " +
			"so that anything this resource stages is submitted, including a plan that only " +
			"reverts a change made outside Terraform.",
		Computed: true,
	}
}

func newRevision() types.String { return types.StringValue(rand.Text()) }

// ensureRevision gives a resource that has never staged through this provider
// version a revision: one that was imported, or whose state predates the
// attribute. A known revision is left alone, because a refresh that moved it
// would submit on every plan.
func ensureRevision(revision *types.String) {
	if revision.IsNull() || revision.IsUnknown() || revision.ValueString() == "" {
		*revision = newRevision()
	}
}

// scopeByID resolves root_scope and resource_id to a scope path. Unlike
// resolveAccountScope it accepts organizational units and roots, for the
// resources that are legitimately set on them.
func scopeByID(ctx context.Context, api *client.Client, rootScope, resourceID types.String) (string, error) {
	entry, err := api.ResolveScope(ctx, rootScope.ValueString(), resourceID.ValueString(), "")
	if err != nil {
		return "", err
	}
	return entry.Scope, nil
}

func diagnoseScopeByID(rootScope, resourceID types.String, err error) diag.Diagnostics {
	if isNotFound(err) {
		var diags diag.Diagnostics
		diags.AddAttributeError(tfpath.Root("resource_id"), "Not found in the cloud hierarchy",
			fmt.Sprintf("Nothing with id %q exists under %s. Give the id of an account, organizational "+
				"unit or the root, without its path.", resourceID.ValueString(), rootScope.ValueString()))
		return diags
	}
	return diagnoseAPIError("Unable to resolve scope", err)
}

// scopeResourceID returns the cloud-native id a scope path ends in, which is
// what resources take as resource_id. It is the id as the hierarchy stores it,
// so a GCP project comes back qualified ("projects|my-project"); resources
// accept that form as well as the bare one.
func scopeResourceID(scope string) string {
	slash := strings.LastIndex(scope, "/")
	if slash < 0 {
		return ""
	}
	return scope[slash+1:]
}

func scopeResourceIDs(scopes []string) []string {
	ids := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		ids = append(ids, scopeResourceID(scope))
	}
	return ids
}

// revisionAfterEdit returns the revision to record after an edit, and whether
// a submission triggered by it runs in this apply.
//
// A revision the plan pinned is kept even when the edit turns out to need a
// redeploy, which happens when the thing was deployed between the plan and the
// apply. Terraform fails an apply that returns a value other than the planned
// one, and the submission was planned against the old value regardless. The
// next refresh sees the outstanding redeploy and moves the revision then.
func revisionAfterEdit(planned, prior types.String, needsDeploy bool) (types.String, bool) {
	switch {
	case !planned.IsUnknown():
		return planned, false
	case needsDeploy:
		return newRevision(), true
	default:
		return prior, false
	}
}

// redeployTiming says when an edit that needs a redeploy is submitted.
func redeployTiming(submitsNow bool) string {
	if submitsNow {
		return "A sonrai_control_submission whose triggers reference this resource's revision does that in " +
			"this apply."
	}
	return "This apply does not submit it: nothing was deployed when the plan was made, so the plan did " +
		"not schedule a submission. Run terraform apply again; the next plan picks the change up and a " +
		"sonrai_control_submission whose triggers reference this resource's revision submits it."
}
