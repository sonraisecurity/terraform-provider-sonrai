package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const disableServicesMutation = `
mutation disableServices($controlKeys: [String!], $scope: String!, $applyAtScope: Boolean!) {
  DisableService(input: {controlKeys: $controlKeys, scope: $scope, applyAtScope: $applyAtScope}) {
    success
    results { controlKey success error }
  }
}`

const unmanageServicesMutation = `
mutation unmanageServices($controlKeys: [String!], $scope: String!) {
  UnmanageService(input: {controlKeys: $controlKeys, scope: $scope}) {
    success
    results { controlKey success error }
  }
}`

const blockedServicesQuery = `
query blockedServices($scope: String!) {
  CloudServices(where: {scope: {op: EQ, value: $scope}}) {
    items {
      controlKey
      status { status isPending }
    }
  }
}`

type bulkServiceResult struct {
	Success bool `json:"success"`
	Results []struct {
		ControlKey string `json:"controlKey"`
		Success    bool   `json:"success"`
		Error      string `json:"error"`
	} `json:"results"`
}

// err returns a combined error naming the services that failed, or nil.
func (r bulkServiceResult) err(operation, scope string) error {
	var failures []string
	for _, item := range r.Results {
		if !item.Success {
			failures = append(failures, fmt.Sprintf("%s (%s)", item.ControlKey, item.Error))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s failed at %s for %v", operation, scope, failures)
	}
	if !r.Success {
		return fmt.Errorf("%s reported failure at %s", operation, scope)
	}
	return nil
}

// DisableServices stages blocks for every control key at scope in one call.
func (c *Client) DisableServices(ctx context.Context, controlKeys []string, scope string) error {
	if len(controlKeys) == 0 {
		return nil
	}
	var out struct {
		DisableService bulkServiceResult `json:"DisableService"`
	}
	vars := map[string]any{
		"controlKeys":  controlKeys,
		"scope":        scope,
		"applyAtScope": false,
	}
	if err := c.Execute(ctx, disableServicesMutation, vars, &out); err != nil {
		return err
	}
	return out.DisableService.err("DisableService", scope)
}

// UnmanageServices stages removal of whatever control covers every control key
// at scope, in one call.
func (c *Client) UnmanageServices(ctx context.Context, controlKeys []string, scope string) error {
	if len(controlKeys) == 0 {
		return nil
	}
	var out struct {
		UnmanageService bulkServiceResult `json:"UnmanageService"`
	}
	vars := map[string]any{
		"controlKeys": controlKeys,
		"scope":       scope,
	}
	if err := c.Execute(ctx, unmanageServicesMutation, vars, &out); err != nil {
		return err
	}
	return out.UnmanageService.err("UnmanageService", scope)
}

// StatusPartialPrefix marks the aggregate statuses CloudServices reports at a
// container scope, such as "Partially Protected".
const StatusPartialPrefix = "Partially"

// BlockedServices returns the control keys currently blocked at scope, sorted.
//
// One query covers every service at the scope, which is what lets an
// account-wide resource read its whole set without a call per service.
//
// scope must name an account. CloudServices aggregates descendants, so at an OU
// or root it reports container statuses such as "Partially Protected" -- which
// carries no control type, appearing even when the only control beneath is a
// disable. Calling this at a container is rejected rather than answered with a
// set that means something different from what the caller assumes.
func (c *Client) BlockedServices(ctx context.Context, scope string) ([]string, error) {
	var out struct {
		CloudServices struct {
			Items []struct {
				ControlKey string        `json:"controlKey"`
				Status     ControlStatus `json:"status"`
			} `json:"items"`
		} `json:"CloudServices"`
	}
	if err := c.Execute(ctx, blockedServicesQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}

	var blocked []string
	for _, item := range out.CloudServices.Items {
		if strings.HasPrefix(item.Status.Status, StatusPartialPrefix) {
			return nil, fmt.Errorf(
				"scope %q reports %q for %s, which only happens at an OU or root: "+
					"BlockedServices requires an account scope",
				scope, item.Status.Status, item.ControlKey)
		}
		if item.Status.Status == StatusDisabled {
			blocked = append(blocked, item.ControlKey)
		}
	}
	sort.Strings(blocked)
	return blocked, nil
}
