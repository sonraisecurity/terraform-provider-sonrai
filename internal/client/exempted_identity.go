package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Exemption types an exempted identity can carry. These are the API's own
// names, used as the wire field names on the mutation input.
const (
	ExemptServiceProtect = "serviceProtect"
	ExemptServiceBlock   = "serviceBlock"
	ExemptQuarantine     = "quarantine"
	ExemptRegionBlock    = "regionBlock"
	ExemptJIT            = "jit"
	ExemptTamperProtect  = "tamperProtect"
)

// ExemptionTypes lists every type in a stable order.
var ExemptionTypes = []string{
	ExemptServiceProtect,
	ExemptServiceBlock,
	ExemptQuarantine,
	ExemptRegionBlock,
	ExemptJIT,
	ExemptTamperProtect,
}

const createStagedExemptedIdentityMutation = `
mutation createStagedExemptedIdentity($input: CreateStagedExemptedIdentityInput) {
  CreateStagedExemptedIdentity(input: $input) {
    success
  }
}`

const deleteStagedExemptedIdentityMutation = `
mutation deleteStagedExemptedIdentity($input: DeleteStagedExemptedIdentityInput) {
  DeleteStagedExemptedIdentity(input: $input) {
    success
    scope
  }
}`

const exemptedIdentitiesQuery = `
query exemptedIdentities($scope: String!) {
  ExemptedIdentities(where: {scope: {op: EQ, value: $scope}}, config: {upstream: false}) {
    items {
      identity
      ssoActorId
      isSSO
      isCoreExemption
      staged
      controlExemptions {
        serviceProtect { exempt staged }
        serviceBlock   { exempt staged }
        quarantine     { exempt staged }
        regionBlock    { exempt staged }
        jit            { exempt staged }
        tamperProtect  { exempt staged }
      }
    }
  }
}`

// ExemptedIdentity is one identity exempted at one scope, with the set of
// control types it is exempt from.
//
// Identity is an ARN pattern, or the SSO actor id when IsSSO. Core exemptions
// are the platform's own built-ins, such as the AWS reserved and StackSet
// execution roles.
type ExemptedIdentity struct {
	Identity string
	IsSSO    bool
	IsCore   bool
	Types    []string

	// Pending is true while the entry, or any of its types, has a change that
	// has not been submitted and deployed.
	Pending bool
}

type exemptionState struct {
	Exempt bool `json:"exempt"`
	Staged bool `json:"staged"`
}

// active reports whether the type is exempt.
//
// exempt and staged are orthogonal: exempt carries the intended state, which a
// staged entry already reflects, and staged says only that the change has yet
// to be deployed. Reading a staged change as exempt regardless would report a
// type staged for removal -- exempt false, staged true -- as still in force.
func (e exemptionState) active() bool { return e.Exempt }

// pending reports whether this type has an undeployed change.
func (e exemptionState) pending() bool { return e.Staged }

type controlExemptions struct {
	ServiceProtect exemptionState `json:"serviceProtect"`
	ServiceBlock   exemptionState `json:"serviceBlock"`
	Quarantine     exemptionState `json:"quarantine"`
	RegionBlock    exemptionState `json:"regionBlock"`
	JIT            exemptionState `json:"jit"`
	TamperProtect  exemptionState `json:"tamperProtect"`
}

func (c controlExemptions) each() map[string]exemptionState {
	return map[string]exemptionState{
		ExemptServiceProtect: c.ServiceProtect,
		ExemptServiceBlock:   c.ServiceBlock,
		ExemptQuarantine:     c.Quarantine,
		ExemptRegionBlock:    c.RegionBlock,
		ExemptJIT:            c.JIT,
		ExemptTamperProtect:  c.TamperProtect,
	}
}

func (c controlExemptions) types() []string {
	out := make([]string, 0, len(ExemptionTypes))
	for name, state := range c.each() {
		if state.active() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (c controlExemptions) anyPending() bool {
	for _, state := range c.each() {
		if state.pending() {
			return true
		}
	}
	return false
}

// StageExemptedIdentity stages identity as exempt from exactly the given types
// at scope. Types not listed are staged off.
//
// The platform allows one staged entry per identity, so every type travels in
// a single call; staging a second time for the same identity is refused with
// FAILED_PRECONDITION.
func (c *Client) StageExemptedIdentity(ctx context.Context, scope, identity string, isSSO bool, types []string) error {
	if err := ValidateExemptedIdentity(identity, isSSO); err != nil {
		return err
	}
	input, err := exemptionInput(scope, identity, isSSO, types)
	if err != nil {
		return err
	}
	input["action"] = "ADD"

	var out struct {
		CreateStagedExemptedIdentity struct {
			Success bool `json:"success"`
		} `json:"CreateStagedExemptedIdentity"`
	}
	// Not retried on an ambiguous failure: a second staging for the same
	// identity is refused with FAILED_PRECONDITION, so a retry after a lost
	// success turns a staged change into a failed apply.
	if err := c.ExecuteOnce(ctx, createStagedExemptedIdentityMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if !out.CreateStagedExemptedIdentity.Success {
		return fmt.Errorf("CreateStagedExemptedIdentity reported failure for %s at %s", identity, scope)
	}
	return nil
}

// RemoveExemptedIdentity stages the removal of an applied exemption.
func (c *Client) RemoveExemptedIdentity(ctx context.Context, scope, identity string, isSSO bool, types []string) error {
	input, err := exemptionInput(scope, identity, isSSO, types)
	if err != nil {
		return err
	}
	input["action"] = "REMOVE"

	var out struct {
		CreateStagedExemptedIdentity struct {
			Success bool `json:"success"`
		} `json:"CreateStagedExemptedIdentity"`
	}
	if err := c.ExecuteOnce(ctx, createStagedExemptedIdentityMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if !out.CreateStagedExemptedIdentity.Success {
		return fmt.Errorf("staging removal of %s at %s reported failure", identity, scope)
	}
	return nil
}

// DiscardStagedExemptedIdentity drops a staged entry without deploying it.
//
// The generic DeletePendingChanges route does not work on exemption rows, so
// an unwanted staged entry has to be withdrawn here or it is applied by
// whatever submission runs next.
func (c *Client) DiscardStagedExemptedIdentity(ctx context.Context, scope, identity string, isSSO bool) error {
	var out struct {
		DeleteStagedExemptedIdentity struct {
			Success bool `json:"success"`
		} `json:"DeleteStagedExemptedIdentity"`
	}
	input := map[string]any{"scope": scope, "isSso": isSSO, "identity": identity}
	if err := c.ExecuteOnce(ctx, deleteStagedExemptedIdentityMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if !out.DeleteStagedExemptedIdentity.Success {
		return fmt.Errorf("DeleteStagedExemptedIdentity reported failure for %s at %s", identity, scope)
	}
	return nil
}

const appliedExemptedIdentitiesQuery = `
query appliedExemptedIdentities($scope: String!, $identity: String!) {
  AppliedExemptedIdentities(where: {scope: {op: EQ, value: $scope}, identity: {op: EQ, value: $identity}}) {
    items { identity isSso }
  }
}`

// ExemptedIdentityDeployed reports whether the exemption for identity at scope
// has been deployed, as opposed to existing only as a staged entry.
//
// The distinction decides how a removal works: a deployed exemption must have
// its removal staged (the platform rejects anything else), while a staged-only
// entry has to be withdrawn outright -- staging a removal alongside it would
// deploy the exemption on its way to undoing it.
func (c *Client) ExemptedIdentityDeployed(ctx context.Context, scope, identity string, isSSO bool) (bool, error) {
	var out struct {
		AppliedExemptedIdentities struct {
			Items []struct {
				Identity string `json:"identity"`
				IsSSO    bool   `json:"isSso"`
			} `json:"items"`
		} `json:"AppliedExemptedIdentities"`
	}
	vars := map[string]any{"scope": scope, "identity": identity}
	if err := c.Execute(ctx, appliedExemptedIdentitiesQuery, vars, &out); err != nil {
		return false, err
	}
	for _, item := range out.AppliedExemptedIdentities.Items {
		if item.IsSSO == isSSO {
			return true, nil
		}
	}
	return false, nil
}

// ExemptedIdentity returns the entry for identity at exactly scope, or
// ErrNotFound.
//
// The query is pinned to upstream: false. An exemption inherited from an
// ancestor is in force at this scope but is not an entry here, and reporting
// one would have Terraform adopt a record it cannot delete.
func (c *Client) ExemptedIdentity(ctx context.Context, scope, identity string, isSSO bool) (*ExemptedIdentity, error) {
	entries, err := c.ExemptedIdentities(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsSSO == isSSO && entry.Identity == identity {
			return &entry, nil
		}
	}
	return nil, fmt.Errorf("%w: exempted identity %q at scope %q", ErrNotFound, identity, scope)
}

// ExemptedIdentities returns every entry defined at exactly scope.
func (c *Client) ExemptedIdentities(ctx context.Context, scope string) ([]ExemptedIdentity, error) {
	var out struct {
		ExemptedIdentities struct {
			Items []struct {
				Identity          string            `json:"identity"`
				SSOActorID        string            `json:"ssoActorId"`
				IsSSO             bool              `json:"isSSO"`
				IsCoreExemption   bool              `json:"isCoreExemption"`
				Staged            bool              `json:"staged"`
				ControlExemptions controlExemptions `json:"controlExemptions"`
			} `json:"items"`
		} `json:"ExemptedIdentities"`
	}
	if err := c.Execute(ctx, exemptedIdentitiesQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}

	entries := make([]ExemptedIdentity, 0, len(out.ExemptedIdentities.Items))
	for _, item := range out.ExemptedIdentities.Items {
		name := item.Identity
		if item.IsSSO {
			name = item.SSOActorID
		}
		if name == "" {
			continue
		}
		entries = append(entries, ExemptedIdentity{
			Identity: name,
			IsSSO:    item.IsSSO,
			IsCore:   item.IsCoreExemption,
			Types:    item.ControlExemptions.types(),
			Pending:  item.Staged || item.ControlExemptions.anyPending(),
		})
	}
	return entries, nil
}

func exemptionInput(scope, identity string, isSSO bool, types []string) (map[string]any, error) {
	input := map[string]any{
		"identity": identity,
		"scope":    scope,
		"isSso":    isSSO,
	}
	for _, name := range ExemptionTypes {
		input[name] = false
	}
	for _, name := range types {
		if _, ok := input[name]; !ok {
			return nil, fmt.Errorf("unknown exemption type %q, want one of %s",
				name, strings.Join(ExemptionTypes, ", "))
		}
		input[name] = true
	}
	return input, nil
}

// ValidateExemptedIdentity refuses an identity that would match far more than
// it names.
//
// Exempted identities are matched as patterns, so a truncated ARN is not
// merely bad input: "arn:aws:iam::" matches every IAM ARN in the organization
// and silently exempts all of them. The platform accepts these — its ARN
// validation lives in the console — so the check has to happen here.
//
// It is deliberately narrow. Short literal patterns such as "sonrai" and
// "*e/stacksets-exec-*" are legitimate and in use; only a string that
// announces itself as an ARN and then names no resource is refused.
func ValidateExemptedIdentity(identity string, isSSO bool) error {
	if strings.TrimSpace(identity) == "" {
		return fmt.Errorf("identity cannot be empty")
	}
	if isSSO || !strings.HasPrefix(identity, "arn:") {
		return nil
	}

	// arn:partition:service:region:account:resource — the resource must name
	// something for the pattern to be narrower than "every ARN of this shape".
	fields := strings.Split(identity, ":")
	resource := ""
	if len(fields) >= 6 {
		resource = strings.TrimSpace(strings.Join(fields[5:], ":"))
	}
	if resource == "" || strings.HasSuffix(resource, "/") || strings.HasSuffix(resource, ":") {
		return fmt.Errorf("%q is an ARN that names no resource; as a pattern it matches "+
			"every ARN of that shape in the organization", identity)
	}
	return nil
}
