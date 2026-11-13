package client

import (
	"context"
	"fmt"
	"sort"
)

// ControlStatus is the status the platform reports for one service at one scope.
type ControlStatus struct {
	// Status is one of Disabled, Protected, Unprotected or Non-Privileged.
	Status string `json:"status"`
	// Pending is true while a staged change for this control has not been
	// submitted and deployed.
	Pending bool `json:"isPending"`
}

// Control statuses the API returns.
const (
	// StatusDisabled is a service whose block is in effect.
	StatusDisabled    = "Disabled"
	StatusProtected   = "Protected"
	StatusUnprotected = "Unprotected"
)

const disableServiceMutation = `
mutation disableService($controlKey: String!, $scope: String!, $applyAtScope: Boolean!) {
  DisableService(input: {controlKey: $controlKey, scope: $scope, applyAtScope: $applyAtScope}) {
    success
  }
}`

const unmanageServiceMutation = `
mutation unmanageService($controlKey: String!, $scope: String!) {
  UnmanageService(input: {controlKey: $controlKey, scope: $scope}) {
    success
  }
}`

const serviceStatusQuery = `
query serviceControlStatus($scope: String!, $controlKey: String!) {
  CloudServiceUsage(where: {scope: {op: EQ, value: $scope}, controlKey: {op: EQ, value: $controlKey}}) {
    items {
      project
      scope
      status {
        status
        isPending
      }
    }
  }
}`

type serviceActionResult struct {
	Success bool `json:"success"`
}

// DisableService stages a service block for controlKey at scope, which may name
// an account or anything above it.
//
// applyAtScope is deliberately fixed to false: the block covers the accounts
// that exist under scope today, and does not extend itself to accounts
// discovered later. Making that configurable is a later decision.
//
// The change is staged, not applied: it takes effect only once the pending
// changes for the containing root scope are submitted and deployed.
func (c *Client) DisableService(ctx context.Context, controlKey, scope string) error {
	var out struct {
		DisableService serviceActionResult `json:"DisableService"`
	}
	vars := map[string]any{
		"controlKey":   controlKey,
		"scope":        scope,
		"applyAtScope": false,
	}
	if err := c.Execute(ctx, disableServiceMutation, vars, &out); err != nil {
		return err
	}
	if !out.DisableService.Success {
		return fmt.Errorf("DisableService reported failure for %s at %s", controlKey, scope)
	}
	return nil
}

// UnmanageService stages removal of whatever control covers controlKey at scope,
// returning the service to unmanaged. It is the inverse of both DisableService
// and ProtectService.
func (c *Client) UnmanageService(ctx context.Context, controlKey, scope string) error {
	var out struct {
		UnmanageService serviceActionResult `json:"UnmanageService"`
	}
	vars := map[string]any{
		"controlKey": controlKey,
		"scope":      scope,
	}
	if err := c.Execute(ctx, unmanageServiceMutation, vars, &out); err != nil {
		return err
	}
	if !out.UnmanageService.Success {
		return fmt.Errorf("UnmanageService reported failure for %s at %s", controlKey, scope)
	}
	return nil
}

// ServiceControlStatus reads the current control status for controlKey at scope.
//
// It returns ErrNotFound when the API knows of no such service at that scope.
// A valid service with no control returns a status rather than an error.
func (c *Client) ServiceControlStatus(ctx context.Context, controlKey, scope string) (*ControlStatus, error) {
	var out struct {
		CloudServiceUsage struct {
			Items []struct {
				Project string        `json:"project"`
				Scope   string        `json:"scope"`
				Status  ControlStatus `json:"status"`
			} `json:"items"`
		} `json:"CloudServiceUsage"`
	}
	vars := map[string]any{
		"scope":      scope,
		"controlKey": controlKey,
	}
	if err := c.Execute(ctx, serviceStatusQuery, vars, &out); err != nil {
		return nil, err
	}
	if len(out.CloudServiceUsage.Items) == 0 {
		return nil, fmt.Errorf("%w: service %q at scope %q", ErrNotFound, controlKey, scope)
	}
	status := out.CloudServiceUsage.Items[0].Status
	return &status, nil
}

const appliedControlKeysQuery = `
query appliedControlKeys($scope: String!) {
  AppliedControlMappings(where: {scope: {op: EQ, value: $scope}}) {
    items { controlKey }
  }
}`

// AppliedControlKeys returns the control keys with a control actually applied
// at scope, as opposed to merely staged.
//
// Status cannot answer this: it reports staged intent, so a control staged but
// never deployed reads the same as one in force. The difference matters when
// removing a control -- unmanaging a staged-only control deletes the staged
// mapping outright and nothing was ever in effect, while unmanaging an applied
// one stages a removal that holds until it is deployed.
func (c *Client) AppliedControlKeys(ctx context.Context, scope string) ([]string, error) {
	var out struct {
		AppliedControlMappings struct {
			Items []struct {
				ControlKey string `json:"controlKey"`
			} `json:"items"`
		} `json:"AppliedControlMappings"`
	}
	if err := c.Execute(ctx, appliedControlKeysQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	var keys []string
	for _, item := range out.AppliedControlMappings.Items {
		if _, ok := seen[item.ControlKey]; ok {
			continue
		}
		seen[item.ControlKey] = struct{}{}
		keys = append(keys, item.ControlKey)
	}
	sort.Strings(keys)
	return keys, nil
}
