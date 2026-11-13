package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const protectServiceMutation = `
mutation protectService($input: ProtectActionInput!) {
  ProtectService(input: $input) {
    success
    issues {
      totalCount
      failureCount
      items { scope message account friendlyName scopeFriendlyName }
    }
  }
}`

const appliedControlExceptionsQuery = `
query appliedControlExceptions($controlKey: String!, $scope: String!) {
  AppliedControlExceptions(where: {
    controlKey: {op: EQ, value: $controlKey}
    scope: {op: EQ, value: $scope}
  }) {
    items { identities ssoActorIds controlId }
  }
}`

const stagedControlExceptionsQuery = `
query stagedControlExceptions($controlKey: String!, $scope: String!) {
  StagedControlExceptions(where: {
    controlKey: {op: EQ, value: $controlKey}
    scope: {op: EQ, value: $scope}
  }) {
    items { identities ssoActorIds action controlId }
  }
}`

// ValidationIssue is one problem the platform reported with an action.
type ValidationIssue struct {
	Scope             string `json:"scope"`
	Message           string `json:"message"`
	Account           string `json:"account"`
	FriendlyName      string `json:"friendlyName"`
	ScopeFriendlyName string `json:"scopeFriendlyName"`
}

// ValidationIssues is the set reported by a single action. A failureCount above
// zero means the action did not fully take.
type ValidationIssues struct {
	TotalCount   int               `json:"totalCount"`
	FailureCount int               `json:"failureCount"`
	Items        []ValidationIssue `json:"items"`
}

// Messages renders the issues for a diagnostic, one per line.
func (v ValidationIssues) Messages() []string {
	out := make([]string, 0, len(v.Items))
	for _, item := range v.Items {
		where := item.ScopeFriendlyName
		if where == "" {
			where = item.Scope
		}
		out = append(out, fmt.Sprintf("%s: %s", where, item.Message))
	}
	return out
}

// ControlExceptions is the set of identities exempt from one control.
//
// Exceptions have no life of their own: the platform removes them when the
// control is unmanaged or switched to a disable. ProtectService adds to them
// but never removes; removal goes through RemoveControlException.
type ControlExceptions struct {
	Identities  []string
	SSOActorIDs []string

	// ControlID identifies the control these exceptions belong to. Empty when
	// the control carries none. Needed to match a PoD grant to the control it
	// exempted, since PoD requests carry the id and not the key.
	ControlID string
}

// ProtectService stages a protect for controlKey at scope, exempting exactly
// the identities given.
//
// skipSuggestedExceptions is fixed to true. Left false, the platform picks its
// own exemptions from recent sensitive-permission usage, which cannot be
// expressed in configuration and makes two applies of the same configuration
// differ. Read the platform's suggestions with SuggestedExceptions and pass the
// ones wanted.
//
// Passing the full sets on every call is deliberate: the platform stages
// whatever is new against the applied set, and replaces this caller's staged
// set outright. It never removes an applied exemption -- that is
// RemoveControlException's job.
func (c *Client) ProtectService(ctx context.Context, controlKey, scope string, identities, ssoActorIDs []string) (*ValidationIssues, error) {
	var out struct {
		ProtectService struct {
			Success bool             `json:"success"`
			Issues  ValidationIssues `json:"issues"`
		} `json:"ProtectService"`
	}
	vars := map[string]any{
		"input": map[string]any{
			"controlKey":              controlKey,
			"scope":                   scope,
			"identities":              nonNil(identities),
			"ssoActorIds":             nonNil(ssoActorIDs),
			"applyAtScope":            false,
			"skipSuggestedExceptions": true,
		},
	}
	if err := c.Execute(ctx, protectServiceMutation, vars, &out); err != nil {
		return nil, err
	}

	issues := out.ProtectService.Issues
	if issues.FailureCount > 0 {
		return &issues, fmt.Errorf("ProtectService reported %d failure(s) for %s at %s: %s",
			issues.FailureCount, controlKey, scope, strings.Join(issues.Messages(), "; "))
	}
	if !out.ProtectService.Success {
		return &issues, fmt.Errorf("ProtectService reported failure for %s at %s", controlKey, scope)
	}
	return &issues, nil
}

const removeExceptionIdentityMutation = `
mutation removeExceptionIdentity($input: ExceptionIdentity) {
  RemoveExceptionIdentity(input: $input) {
    response
  }
}`

// RemoveControlException stages the removal of one identity's exemption from
// an applied control.
//
// ProtectService cannot do this: on the platform it is add-only against the
// applied exception set, so an identity dropped from the configured list stays
// exempt until removed here.
//
// forceStaged is fixed to true. Without it the platform applies the removal
// through an immediate transaction of its own, outside the stage-then-submit
// flow every other change in this provider follows.
func (c *Client) RemoveControlException(ctx context.Context, rootScope, scope, controlID, controlKey, identity string, isSSO bool) error {
	var out struct {
		RemoveExceptionIdentity struct {
			Response string `json:"response"`
		} `json:"RemoveExceptionIdentity"`
	}
	vars := map[string]any{
		"input": map[string]any{
			"rootScope":   rootScope,
			"scope":       scope,
			"controlId":   controlID,
			"controlKey":  controlKey,
			"identity":    identity,
			"isSSO":       isSSO,
			"forceStaged": true,
		},
	}
	return c.ExecuteOnce(ctx, removeExceptionIdentityMutation, vars, &out)
}

// ControlExceptions returns the identities exempt from controlKey at scope,
// reflecting staged changes as well as what is applied.
//
// This reads AppliedControlExceptions and StagedControlExceptions rather than
// the appliedExceptionList on CloudServiceUsage: that list is a display
// projection which renders SSO identities as display names, so it cannot be
// compared with what was written.
func (c *Client) ControlExceptions(ctx context.Context, controlKey, scope string) (*ControlExceptions, error) {
	vars := map[string]any{"controlKey": controlKey, "scope": scope}

	var applied struct {
		AppliedControlExceptions struct {
			Items []struct {
				Identities  []string `json:"identities"`
				SSOActorIDs []string `json:"ssoActorIds"`
				ControlID   string   `json:"controlId"`
			} `json:"items"`
		} `json:"AppliedControlExceptions"`
	}
	if err := c.Execute(ctx, appliedControlExceptionsQuery, vars, &applied); err != nil {
		return nil, err
	}

	identities := stringSet{}
	ssoActors := stringSet{}
	controlID := ""
	for _, item := range applied.AppliedControlExceptions.Items {
		identities.add(item.Identities...)
		ssoActors.add(item.SSOActorIDs...)
		if controlID == "" {
			controlID = item.ControlID
		}
	}

	var staged struct {
		StagedControlExceptions struct {
			Items []struct {
				Identities  []string `json:"identities"`
				SSOActorIDs []string `json:"ssoActorIds"`
				Action      string   `json:"action"`
				ControlID   string   `json:"controlId"`
			} `json:"items"`
		} `json:"StagedControlExceptions"`
	}
	if err := c.Execute(ctx, stagedControlExceptionsQuery, vars, &staged); err != nil {
		return nil, err
	}

	// Staged changes are the intent, matching how a control's status reads
	// Protected from the moment it is staged.
	for _, item := range staged.StagedControlExceptions.Items {
		if controlID == "" {
			controlID = item.ControlID
		}
		if strings.EqualFold(item.Action, "REMOVE") {
			identities.remove(item.Identities...)
			ssoActors.remove(item.SSOActorIDs...)
			continue
		}
		identities.add(item.Identities...)
		ssoActors.add(item.SSOActorIDs...)
	}

	return &ControlExceptions{
		Identities:  identities.sorted(),
		SSOActorIDs: ssoActors.sorted(),
		ControlID:   controlID,
	}, nil
}

// stringSet merges applied and staged entries before they are sorted.
type stringSet map[string]struct{}

func (u stringSet) add(values ...string) {
	for _, v := range values {
		if v != "" {
			u[v] = struct{}{}
		}
	}
}

func (u stringSet) remove(values ...string) {
	for _, v := range values {
		delete(u, v)
	}
}

func (u stringSet) sorted() []string {
	out := make([]string, 0, len(u))
	for v := range u {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// nonNil renders a nil slice as an empty list so the API receives [] rather
// than null, which it treats as "no exemptions" rather than "unset".
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

const suggestedExceptionsQuery = `
query suggestedExceptions($scope: String!, $controlKey: String!) {
  CloudServiceUsage(where: {
    scope: {op: EQ, value: $scope}
    controlKey: {op: EQ, value: $controlKey}
  }) {
    items {
      suggestedExceptionList {
        resourceId
        ssoActorId
        name
        isSSO
        lastSensitiveUsed
        lastSensitiveUsedDateString
      }
    }
  }
}`

// SuggestedException is one identity the platform would exempt when protecting
// a service, because it recently used one of that service's sensitive
// permissions.
type SuggestedException struct {
	ResourceID   string `json:"resourceId"`
	SSOActorID   string `json:"ssoActorId"`
	Name         string `json:"name"`
	IsSSO        bool   `json:"isSSO"`
	LastUsed     string `json:"lastSensitiveUsed"`
	LastUsedDate string `json:"lastSensitiveUsedDateString"`
}

// SuggestedExceptions returns the identities the platform would exempt when
// protecting controlKey at scope.
//
// The lookback window is an organization setting, so this set changes over time
// even when nothing else does.
func (c *Client) SuggestedExceptions(ctx context.Context, controlKey, scope string) ([]SuggestedException, error) {
	var out struct {
		CloudServiceUsage struct {
			Items []struct {
				SuggestedExceptionList []SuggestedException `json:"suggestedExceptionList"`
			} `json:"items"`
		} `json:"CloudServiceUsage"`
	}
	vars := map[string]any{"scope": scope, "controlKey": controlKey}
	if err := c.Execute(ctx, suggestedExceptionsQuery, vars, &out); err != nil {
		return nil, err
	}
	if len(out.CloudServiceUsage.Items) == 0 {
		return nil, fmt.Errorf("%w: service %q at scope %q", ErrNotFound, controlKey, scope)
	}
	return out.CloudServiceUsage.Items[0].SuggestedExceptionList, nil
}
