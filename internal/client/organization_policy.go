package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The policy types an organization policy can be written as.
const (
	ServiceControlPolicy  = "SERVICE_CONTROL_POLICY"
	ResourceControlPolicy = "RESOURCE_CONTROL_POLICY"
)

// OrganizationPolicyTypes lists the policy types that can be authored.
var OrganizationPolicyTypes = []string{ServiceControlPolicy, ResourceControlPolicy}

// The platform keeps this prefix for the policies it deploys itself.
const ReservedPolicyNamePrefix = "Sonrai"

const (
	policySourceAuthored = "authored"
	policyStateManaged   = "ORGANIZATION_POLICY_MANAGED"

	policyMappingAdd    = "ADD"
	policyMappingRemove = "REMOVE"

	organizationPolicySyncChange = "organization_policy_sync"
)

// OrganizationPolicy is an AWS organization policy the platform knows about:
// one written in Sonrai, or one it discovered in the AWS organization.
type OrganizationPolicy struct {
	ID          string
	Name        string
	Description string
	Type        string
	// Content is the policy document as the platform stores it: compact JSON.
	Content string
	// Authored is true for a policy written in Sonrai, which exists nowhere
	// else, and false for one discovered in AWS.
	Authored bool
	// Managed is false for a discovered policy Sonrai only reports on.
	Managed bool
	// AttachedScopes are the topmost scopes the policy is deployed at.
	AttachedScopes []string
}

// PolicyMapping is one scope an organization policy reaches, or is staged to
// reach or leave. The platform keeps a row for every scope under an
// attachment, not only the one it was attached at, and does not say which is
// which.
type PolicyMapping struct {
	RootScope     string `json:"rootScope"`
	Scope         string `json:"scope"`
	Action        string `json:"action"`
	InTransaction bool   `json:"inTransaction"`
}

// OrganizationPolicyDetail is a managed policy with where it is deployed and
// what this identity has staged for it. Changes staged by anyone else are not
// visible.
type OrganizationPolicyDetail struct {
	Policy  OrganizationPolicy
	Staged  []PolicyMapping
	Applied []PolicyMapping
}

// Deployed reports whether the policy is in force anywhere.
func (d *OrganizationPolicyDetail) Deployed() bool { return len(d.Applied) > 0 }

// AttachedAt reports whether the policy reaches scope once what this identity
// has staged is deployed.
func (d *OrganizationPolicyDetail) AttachedAt(scope string) bool {
	var applied, adding, removing bool
	for _, m := range d.Applied {
		applied = applied || m.Scope == scope
	}
	for _, m := range d.Staged {
		if m.Scope != scope {
			continue
		}
		adding = adding || m.Action == policyMappingAdd
		removing = removing || m.Action == policyMappingRemove
	}
	return adding || (applied && !removing)
}

// DeployedAt reports whether the policy is in force at scope today, whatever
// is staged there.
func (d *OrganizationPolicyDetail) DeployedAt(scope string) bool {
	for _, m := range d.Applied {
		if m.Scope == scope {
			return true
		}
	}
	return false
}

// DeployedScopes lists the topmost scopes the policy is deployed at.
func (d *OrganizationPolicyDetail) DeployedScopes() []string {
	scopes := make([]string, 0, len(d.Applied))
	for _, m := range d.Applied {
		scopes = append(scopes, m.Scope)
	}
	return topmostScopes(scopes)
}

// DeployedRootScopes lists the root scopes the policy is deployed under.
func (d *OrganizationPolicyDetail) DeployedRootScopes() []string {
	seen := map[string]struct{}{}
	var roots []string
	for _, m := range d.Applied {
		if _, ok := seen[m.RootScope]; ok || m.RootScope == "" {
			continue
		}
		seen[m.RootScope] = struct{}{}
		roots = append(roots, m.RootScope)
	}
	sort.Strings(roots)
	return roots
}

// topmostScopes drops every scope that lies beneath another in the list.
func topmostScopes(scopes []string) []string {
	sorted := append([]string(nil), scopes...)
	sort.Strings(sorted)
	out := make([]string, 0, len(sorted))
	for _, scope := range sorted {
		if len(out) > 0 && (scope == out[len(out)-1] || strings.HasPrefix(scope, out[len(out)-1]+"/")) {
			continue
		}
		out = append(out, scope)
	}
	return out
}

type organizationPolicyRow struct {
	ID                string          `json:"id"`
	PolicyName        string          `json:"policyName"`
	PolicyDescription string          `json:"policyDescription"`
	PolicyType        string          `json:"policyType"`
	CurrentContent    string          `json:"currentContent"`
	Source            string          `json:"source"`
	State             string          `json:"state"`
	AttachedTargets   []string        `json:"attachedTargets"`
	AppliedMappings   []PolicyMapping `json:"appliedMappings"`
}

func (r organizationPolicyRow) policy() OrganizationPolicy {
	attached := r.AttachedTargets
	if len(r.AppliedMappings) > 0 {
		attached = attached[:0:0]
		for _, m := range r.AppliedMappings {
			attached = append(attached, m.Scope)
		}
	}
	return OrganizationPolicy{
		ID:             r.ID,
		Name:           r.PolicyName,
		Description:    r.PolicyDescription,
		Type:           r.PolicyType,
		Content:        r.CurrentContent,
		Authored:       r.Source == policySourceAuthored,
		Managed:        r.State == policyStateManaged,
		AttachedScopes: topmostScopes(attached),
	}
}

// NewOrganizationPolicy is what an authored policy is created from.
type NewOrganizationPolicy struct {
	Name        string
	Description string
	Type        string
	Content     string
	// Scope is the AWS organization the policy is written for, as a root scope.
	Scope string
}

const createOrganizationPolicyMutation = `
mutation createOrganizationPolicy($input: CreateOrganizationPolicyInput!) {
  CreateOrganizationPolicy(input: $input) {
    success
    policy { id policyName policyDescription policyType currentContent source state }
  }
}`

// CreateOrganizationPolicy writes a new policy. It takes effect in Sonrai at
// once and is in force nowhere until it is attached and that is deployed.
//
// The platform does not keep names unique, so this never fails for a name
// that is already taken; callers that care have to look first.
func (c *Client) CreateOrganizationPolicy(ctx context.Context, policy NewOrganizationPolicy) (*OrganizationPolicy, error) {
	if err := validateOrganizationPolicy(policy.Name, policy.Type, policy.Content); err != nil {
		return nil, err
	}
	if policy.Scope == "" {
		return nil, errors.New("an organization policy needs the root scope it is written for")
	}
	input := map[string]any{
		"policyName":        policy.Name,
		"policyType":        policy.Type,
		"policyDescription": policy.Description,
		"content":           policy.Content,
		"rootScope":         policy.Scope,
	}
	var out struct {
		CreateOrganizationPolicy struct {
			Success bool                   `json:"success"`
			Policy  *organizationPolicyRow `json:"policy"`
		} `json:"CreateOrganizationPolicy"`
	}
	// Not retried on an ambiguous failure: a retry after a lost success would
	// make a second policy with the same name.
	if err := c.ExecuteOnce(ctx, createOrganizationPolicyMutation, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	result := out.CreateOrganizationPolicy
	if !result.Success || result.Policy == nil || result.Policy.ID == "" {
		return nil, fmt.Errorf("CreateOrganizationPolicy reported failure for %q", policy.Name)
	}
	created := result.Policy.policy()
	return &created, nil
}

const organizationPolicyQuery = `
query organizationPolicy($id: String!) {
  GetOrganizationPolicy(where: {id: $id}) {
    policy { id policyName policyDescription policyType currentContent source state }
    stagedMappings { rootScope scope action inTransaction }
    appliedMappings { rootScope scope action inTransaction }
  }
}`

// OrganizationPolicy reads one managed policy by id. It returns ErrNotFound
// when the platform manages no policy with that id.
func (c *Client) OrganizationPolicy(ctx context.Context, id string) (*OrganizationPolicyDetail, error) {
	if id == "" {
		return nil, errors.New("an organization policy id is required")
	}
	var out struct {
		GetOrganizationPolicy struct {
			Policy          *organizationPolicyRow `json:"policy"`
			StagedMappings  []PolicyMapping        `json:"stagedMappings"`
			AppliedMappings []PolicyMapping        `json:"appliedMappings"`
		} `json:"GetOrganizationPolicy"`
	}
	if err := c.Execute(ctx, organizationPolicyQuery, map[string]any{"id": id}, &out); err != nil {
		if strings.Contains(err.Error(), "managed policy not found") {
			return nil, fmt.Errorf("%w: organization policy %q", ErrNotFound, id)
		}
		return nil, err
	}
	got := out.GetOrganizationPolicy
	if got.Policy == nil || got.Policy.ID != id {
		return nil, fmt.Errorf("%w: organization policy %q", ErrNotFound, id)
	}
	detail := &OrganizationPolicyDetail{
		Policy:  got.Policy.policy(),
		Staged:  got.StagedMappings,
		Applied: got.AppliedMappings,
	}
	detail.Policy.AttachedScopes = detail.DeployedScopes()
	return detail, nil
}

const updateOrganizationPolicyMutation = `
mutation updateOrganizationPolicy($input: UpdateOrganizationPolicyInput!) {
  UpdateOrganizationPolicy(input: $input) {
    success
  }
}`

// UpdateOrganizationPolicy replaces the name, description and whole document
// of a managed policy.
//
// The write is immediate in Sonrai. Where the policy is already deployed the
// old document stays in force until that root scope is deployed again. The
// platform reads an empty name or description as "leave it alone", so neither
// can be cleared.
func (c *Client) UpdateOrganizationPolicy(ctx context.Context, id string, policy NewOrganizationPolicy) error {
	if id == "" {
		return errors.New("an organization policy id is required")
	}
	if err := validateOrganizationPolicy(policy.Name, policy.Type, policy.Content); err != nil {
		return err
	}
	input := map[string]any{
		"id":                id,
		"policyName":        policy.Name,
		"policyDescription": policy.Description,
		"policyJson":        policy.Content,
	}
	var out struct {
		UpdateOrganizationPolicy struct {
			Success bool `json:"success"`
		} `json:"UpdateOrganizationPolicy"`
	}
	if err := c.Execute(ctx, updateOrganizationPolicyMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if !out.UpdateOrganizationPolicy.Success {
		return fmt.Errorf("UpdateOrganizationPolicy reported failure for %q", id)
	}
	return nil
}

// PolicyStillLiveError reports that a policy could not be removed because it
// is deployed, or mid-deployment, at the scopes named.
type PolicyStillLiveError struct {
	Message string
	Scopes  []string
}

func (e *PolicyStillLiveError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "organization policy is still attached at " + strings.Join(e.Scopes, ", ")
}

const unmanageOrganizationPolicyMutation = `
mutation unmanageOrganizationPolicy($input: UnmanageOrganizationPolicyInput!) {
  UnmanageOrganizationPolicy(input: $input) {
    success
    message
    attachments { rootScope scopes }
  }
}`

// UnmanageOrganizationPolicy removes a policy that is deployed nowhere. An
// authored policy is deleted outright; a discovered one goes back to being
// only reported on. Attachments that were staged and never deployed are
// discarded with it.
//
// The platform answers a policy that is still deployed with success false
// rather than an error, which comes back as a *PolicyStillLiveError.
//
// The platform's DeleteOrganizationPolicy is deliberately not used for a
// deployed policy. It stages the removal everywhere and marks the policy for
// deletion, but once that deploys the policy is left in place, detached.
func (c *Client) UnmanageOrganizationPolicy(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("an organization policy id is required")
	}
	var out struct {
		UnmanageOrganizationPolicy struct {
			Success     bool   `json:"success"`
			Message     string `json:"message"`
			Attachments []struct {
				Scopes []string `json:"scopes"`
			} `json:"attachments"`
		} `json:"UnmanageOrganizationPolicy"`
	}
	if err := c.ExecuteOnce(ctx, unmanageOrganizationPolicyMutation, map[string]any{"input": map[string]any{"id": id}}, &out); err != nil {
		return err
	}
	result := out.UnmanageOrganizationPolicy
	if result.Success {
		return nil
	}
	live := &PolicyStillLiveError{Message: result.Message}
	for _, attachment := range result.Attachments {
		live.Scopes = append(live.Scopes, attachment.Scopes...)
	}
	live.Scopes = topmostScopes(live.Scopes)
	return live
}

const attachOrganizationPolicyMutation = `
mutation attachOrganizationPolicy($input: AttachOrganizationPolicyInput!) {
  AttachOrganizationPolicy(input: $input) {
    success
  }
}`

const detachOrganizationPolicyMutation = `
mutation detachOrganizationPolicy($input: DetachOrganizationPolicyInput!) {
  DetachOrganizationPolicy(input: $input) {
    success
  }
}`

// AttachOrganizationPolicy stages the policy at scope and everything beneath
// it. Nothing is in force until that is submitted and deployed. Attaching
// where the policy already reaches stages nothing.
func (c *Client) AttachOrganizationPolicy(ctx context.Context, id, scope string) error {
	return c.movePolicy(ctx, attachOrganizationPolicyMutation, "AttachOrganizationPolicy", id, scope)
}

// DetachOrganizationPolicy stages the policy's removal from scope and
// everything beneath it, or withdraws an attachment that was staged and never
// deployed. The platform refuses while a scope above still attaches it.
func (c *Client) DetachOrganizationPolicy(ctx context.Context, id, scope string) error {
	return c.movePolicy(ctx, detachOrganizationPolicyMutation, "DetachOrganizationPolicy", id, scope)
}

func (c *Client) movePolicy(ctx context.Context, mutation, operation, id, scope string) error {
	if id == "" || scope == "" {
		return errors.New("an organization policy id and a scope are required")
	}
	// applyAtScope is what makes this one attachment at the scope itself, the
	// way AWS attaches a policy. Without it the platform attaches to each
	// account beneath separately, and accounts added later are missed.
	input := map[string]any{"id": id, "scope": scope, "applyAtScope": true}
	out := map[string]struct {
		Success bool `json:"success"`
	}{}
	if err := c.Execute(ctx, mutation, map[string]any{"input": input}, &out); err != nil {
		if strings.Contains(err.Error(), "managed policy not found") {
			return fmt.Errorf("%w: organization policy %q", ErrNotFound, id)
		}
		return err
	}
	if !out[operation].Success {
		return fmt.Errorf("%s reported failure for policy %q at %s", operation, id, scope)
	}
	return nil
}

// PolicyFinding is one problem the platform found in a policy document.
type PolicyFinding struct {
	Details string `json:"findingDetails"`
	Type    string `json:"findingType"`
	Code    string `json:"issueCode"`
}

// Blocking reports whether the finding would stop the policy being accepted.
func (f PolicyFinding) Blocking() bool { return f.Type == "FINDING_ERROR" }

// PolicyReview is the platform's assessment of a policy document.
type PolicyReview struct {
	Findings []PolicyFinding
	// Size and SizeLimit are in characters, as AWS counts a policy.
	Size      int
	SizeLimit int
}

const reviewOrganizationPolicyQuery = `
query reviewOrganizationPolicy($input: ReviewOrganizationPolicyInput!) {
  ReviewOrganizationPolicy(input: $input) {
    validationFindings { findingDetails findingType issueCode }
    sizeCharacters
    sizeLimitCharacters
  }
}`

// ReviewOrganizationPolicy has the platform check a policy document without
// writing anything. A create or update of a document it would reject fails
// with an error that says nothing, so this is where the reasons come from.
func (c *Client) ReviewOrganizationPolicy(ctx context.Context, policyType, content, scope string) (*PolicyReview, error) {
	if !knownPolicyType(policyType) {
		return nil, fmt.Errorf("unknown organization policy type %q", policyType)
	}
	input := map[string]any{
		"policyDocument": content,
		"policyType":     policyType,
		"targetScopes":   []string{scope},
	}
	var out struct {
		ReviewOrganizationPolicy struct {
			ValidationFindings  []PolicyFinding `json:"validationFindings"`
			SizeCharacters      int             `json:"sizeCharacters"`
			SizeLimitCharacters int             `json:"sizeLimitCharacters"`
		} `json:"ReviewOrganizationPolicy"`
	}
	if err := c.Execute(ctx, reviewOrganizationPolicyQuery, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	review := out.ReviewOrganizationPolicy
	return &PolicyReview{
		Findings:  review.ValidationFindings,
		Size:      review.SizeCharacters,
		SizeLimit: review.SizeLimitCharacters,
	}, nil
}

const organizationPoliciesQuery = `
query organizationPolicies($rootScope: String!, $limit: Long, $offset: Long) {
  ListOrganizationPolicies(where: {rootScope: {op: EQ, value: $rootScope}}) {
    items(limit: $limit, offset: $offset) {
      id policyName policyDescription policyType currentContent source state attachedTargets
      appliedMappings { rootScope scope }
    }
  }
}`

const (
	organizationPoliciesPageSize = 100
	organizationPoliciesMaxPages = 50
)

// OrganizationPolicies lists every organization policy the platform knows of
// under an AWS root scope, managed or not, sorted by name.
func (c *Client) OrganizationPolicies(ctx context.Context, rootScope string) ([]OrganizationPolicy, error) {
	if !strings.HasPrefix(rootScope, "aws/") {
		return nil, fmt.Errorf("organization policies are only available for AWS, not %q", rootScope)
	}
	seen := map[string]struct{}{}
	var policies []OrganizationPolicy
	for page := 0; ; page++ {
		if page == organizationPoliciesMaxPages {
			return nil, fmt.Errorf("more than %d organization policies under %q; refusing to work from a partial list",
				organizationPoliciesPageSize*organizationPoliciesMaxPages, rootScope)
		}
		var out struct {
			ListOrganizationPolicies struct {
				Items []organizationPolicyRow `json:"items"`
			} `json:"ListOrganizationPolicies"`
		}
		vars := map[string]any{
			"rootScope": rootScope,
			"limit":     organizationPoliciesPageSize,
			"offset":    page * organizationPoliciesPageSize,
		}
		if err := c.Execute(ctx, organizationPoliciesQuery, vars, &out); err != nil {
			return nil, err
		}
		for _, row := range out.ListOrganizationPolicies.Items {
			if _, ok := seen[row.ID]; ok {
				continue
			}
			seen[row.ID] = struct{}{}
			policies = append(policies, row.policy())
		}
		if len(out.ListOrganizationPolicies.Items) < organizationPoliciesPageSize {
			break
		}
	}
	sort.SliceStable(policies, func(i, j int) bool {
		if policies[i].Name != policies[j].Name {
			return policies[i].Name < policies[j].Name
		}
		return policies[i].ID < policies[j].ID
	})
	return policies, nil
}

// ManagedOrganizationPoliciesNamed returns the managed policies under
// rootScope that carry this exact name. The platform allows several.
func (c *Client) ManagedOrganizationPoliciesNamed(ctx context.Context, rootScope, name string) ([]OrganizationPolicy, error) {
	all, err := c.OrganizationPolicies(ctx, rootScope)
	if err != nil {
		return nil, err
	}
	var named []OrganizationPolicy
	for _, policy := range all {
		if policy.Managed && policy.Name == name {
			named = append(named, policy)
		}
	}
	return named, nil
}

// OrganizationPolicySyncPending reports whether an edit to, or the deletion
// of, the policy is still waiting to be deployed under rootScope.
func (c *Client) OrganizationPolicySyncPending(ctx context.Context, rootScope, id string) (bool, error) {
	pending, err := c.PendingChanges(ctx, rootScope)
	if err != nil {
		return false, err
	}
	for _, change := range pending {
		if change.ChangeType == organizationPolicySyncChange && (change.ControlKey == id || change.ID == id) {
			return true, nil
		}
	}
	return false, nil
}

func knownPolicyType(policyType string) bool {
	for _, known := range OrganizationPolicyTypes {
		if policyType == known {
			return true
		}
	}
	return false
}

func validateOrganizationPolicy(name, policyType, content string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("an organization policy needs a name")
	}
	if strings.HasPrefix(name, ReservedPolicyNamePrefix) {
		return fmt.Errorf("an organization policy name cannot start with %q, which is reserved", ReservedPolicyNamePrefix)
	}
	if !knownPolicyType(policyType) {
		return fmt.Errorf("unknown organization policy type %q; want one of %s",
			policyType, strings.Join(OrganizationPolicyTypes, ", "))
	}
	if strings.TrimSpace(content) == "" {
		return errors.New("an organization policy needs a policy document")
	}
	return nil
}
