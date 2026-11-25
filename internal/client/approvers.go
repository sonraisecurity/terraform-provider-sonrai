package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Approval types, which decide who may approve a request at a scope.
const (
	// ApprovalExcludeSelf lets assigned approvers approve, but never their own
	// request. It is what the platform gives a scope nobody has configured.
	ApprovalExcludeSelf = "EXCLUDE_SELF"
	// ApprovalAssignedApprovers lets assigned approvers approve, their own
	// requests included.
	ApprovalAssignedApprovers = "ASSIGNED_APPROVERS"
	// ApprovalEveryoneAtScope lets anyone approve, their own requests included.
	ApprovalEveryoneAtScope = "EVERYONE_AT_SCOPE"
)

// ApprovalTypes lists every approval type the API accepts.
var ApprovalTypes = []string{ApprovalExcludeSelf, ApprovalAssignedApprovers, ApprovalEveryoneAtScope}

// ScopeApprovers is the approval configuration set directly on one scope.
type ScopeApprovers struct {
	Scope string
	// Emails are the approvers assigned at this scope itself, lower-cased and
	// sorted. Approvers inherited from a parent scope are not included.
	Emails       []string
	ApprovalType string
	// Exceptions counts the conditional approver rules on the scope. They are
	// not modelled here, but every write removes them, so callers need to know
	// when there are some to lose.
	Exceptions int
}

const scopeApproversQuery = `
query scopeApprovers($filters: CloudHierarchyFilter) {
  CloudHierarchyList(where: $filters) {
    items {
      scope
      approvalType
      owners { email }
      exceptions { appliesToApprovals }
    }
  }
}`

const assignScopeOwnersMutation = `
mutation assignScopeOwners($input: AssignScopeOwnersInput) {
  AssignScopeOwners(input: $input) {
    success
    error
  }
}`

// ScopeApprovers reads the approvers and approval type set on scope. It
// returns ErrNotFound when the hierarchy has no such scope.
func (c *Client) ScopeApprovers(ctx context.Context, scope string) (*ScopeApprovers, error) {
	var out struct {
		CloudHierarchyList struct {
			Items []struct {
				Scope        string `json:"scope"`
				ApprovalType string `json:"approvalType"`
				Owners       []struct {
					Email string `json:"email"`
				} `json:"owners"`
				Exceptions []struct {
					AppliesToApprovals bool `json:"appliesToApprovals"`
				} `json:"exceptions"`
			} `json:"items"`
		} `json:"CloudHierarchyList"`
	}
	vars := map[string]any{"filters": map[string]any{
		"scope": map[string]any{"op": "EQ", "value": scope},
	}}
	if err := c.Execute(ctx, scopeApproversQuery, vars, &out); err != nil {
		return nil, err
	}
	for _, item := range out.CloudHierarchyList.Items {
		// The filter is EQ; a row for any other scope is not this scope's
		// configuration and must not be reported as if it were.
		if item.Scope != scope {
			continue
		}
		approvers := &ScopeApprovers{
			Scope:        item.Scope,
			ApprovalType: item.ApprovalType,
			Exceptions:   len(item.Exceptions),
			Emails:       make([]string, 0, len(item.Owners)),
		}
		if approvers.ApprovalType == "" {
			approvers.ApprovalType = ApprovalExcludeSelf
		}
		for _, owner := range item.Owners {
			if owner.Email != "" {
				approvers.Emails = append(approvers.Emails, strings.ToLower(owner.Email))
			}
		}
		sort.Strings(approvers.Emails)
		return approvers, nil
	}
	return nil, fmt.Errorf("%w: scope %q", ErrNotFound, scope)
}

// AssignScopeApprovers replaces the approvers set directly on scope with
// emails, and sets its approval type when approvalType is not empty.
//
// It takes effect immediately; nothing is staged. Beyond the scope itself:
//
//   - Every conditional approver rule (exception) on the scope is removed,
//     whether or not any is sent.
//   - An email that is not yet a Sonrai user is invited, and an approver who
//     lacks an approver role is granted one. Neither is undone when the
//     approver is later removed.
//   - The approval type is also written to descendant scopes that have no
//     approvers of their own.
//
// Descendants keep their own approvers: overrideChildScopes stays false,
// because true deletes the approvers and exceptions of every scope beneath.
//
// The invites and role grants are why this is sent once rather than retried.
func (c *Client) AssignScopeApprovers(ctx context.Context, scope string, emails []string, approvalType string) error {
	owners := make([]map[string]any, 0, len(emails))
	for _, email := range emails {
		owners = append(owners, map[string]any{"email": email})
	}
	input := map[string]any{
		"scope":               scope,
		"owners":              owners,
		"overrideChildScopes": false,
	}
	if approvalType != "" {
		input["approvalType"] = approvalType
	}

	var out struct {
		AssignScopeOwners struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"AssignScopeOwners"`
	}
	if err := c.ExecuteOnce(ctx, assignScopeOwnersMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	// The platform reports some refusals in the payload with no GraphQL error.
	if !out.AssignScopeOwners.Success {
		reason := out.AssignScopeOwners.Error
		if reason == "" {
			reason = "no reason given"
		}
		return fmt.Errorf("AssignScopeOwners reported failure for %s: %s", scope, reason)
	}
	return nil
}
