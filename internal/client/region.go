package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Region statuses the platform reports at a scope.
//
// The partial values appear at a container whose children disagree: disabling
// a region at a root and enabling it again at one account leaves the root
// PARTIALLY_DISABLED.
const (
	RegionEnabled           = "ENABLED"
	RegionDisabled          = "DISABLED"
	RegionPartiallyEnabled  = "PARTIALLY_ENABLED"
	RegionPartiallyDisabled = "PARTIALLY_DISABLED"
)

// The scope filter is spelled with a capital S, matching the field name on
// RegionControlFilter. Lowercase is rejected.
//
// The inheritance fields (isInherited, inheritedFromScope) are deliberately
// not selected: nothing here consumes them, and deployed servers can predate
// them, where selecting one fails the whole query with a validation error.
const regionControlsQuery = `
query regionsControls($scope: String!) {
  RegionsControls(where: {Scope: {op: EQ, value: $scope}}) {
    items {
      regionId
      regionName
      cloudType
      criticalRegion
      status { status pending }
    }
  }
}`

const disableRegionMutation = `
mutation disableRegion($input: RegionMutationInput) {
  DisableRegion(input: $input) {
    success
  }
}`

// EnableRegion takes no applyAtScope. The field exists on RegionMutationInput
// but the enable path rejects it server-side, so it is never sent.
const enableRegionMutation = `
mutation enableRegion($input: RegionMutationInput) {
  EnableRegion(input: $input) {
    success
  }
}`

// RegionControl is one region's control state at one scope.
type RegionControl struct {
	RegionID   string
	RegionName string
	CloudType  string
	Status     string
	Pending    bool

	// Critical marks a region the platform refuses to disable.
	Critical bool
}

// RegionControls lists every region visible at exactly scope.
func (c *Client) RegionControls(ctx context.Context, scope string) ([]RegionControl, error) {
	var out struct {
		RegionsControls struct {
			Items []struct {
				RegionID       string `json:"regionId"`
				RegionName     string `json:"regionName"`
				CloudType      string `json:"cloudType"`
				CriticalRegion bool   `json:"criticalRegion"`
				Status         struct {
					Status  string `json:"status"`
					Pending bool   `json:"pending"`
				} `json:"status"`
			} `json:"items"`
		} `json:"RegionsControls"`
	}
	if err := c.Execute(ctx, regionControlsQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}

	regions := make([]RegionControl, 0, len(out.RegionsControls.Items))
	for _, item := range out.RegionsControls.Items {
		if item.RegionID == "" {
			continue
		}
		regions = append(regions, RegionControl{
			RegionID:   item.RegionID,
			RegionName: item.RegionName,
			CloudType:  item.CloudType,
			Status:     strings.ToUpper(strings.TrimSpace(item.Status.Status)),
			Pending:    item.Status.Pending,
			Critical:   item.CriticalRegion,
		})
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].RegionID < regions[j].RegionID })
	return regions, nil
}

// RegionControl returns one region's state at scope, or ErrNotFound when the
// scope has no such region.
//
// A region absent here is one the account does not have enabled in the cloud:
// the platform's view comes from what the reconciler found, so there is
// nothing to control.
func (c *Client) RegionControl(ctx context.Context, scope, regionID string) (*RegionControl, error) {
	regions, err := c.RegionControls(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, region := range regions {
		if region.RegionID == regionID {
			return &region, nil
		}
	}
	return nil, fmt.Errorf("%w: region %q at scope %q", ErrNotFound, regionID, scope)
}

// DisableRegion stages a disable for regionID at scope. Disabling at a
// container already stages a disable at every leaf scope beneath it.
//
// The input's applyAtScope field is never sent: region controls do not use
// it, and servers that predate the field reject unknown input keys.
//
// The platform answers success without staging anything when the region is not
// enabled at the scope, so a caller that needs to know a disable took effect
// has to read the status back.
//
// Retried like the other desired-state control mutations: repeating the call
// re-states the same desired state rather than stacking a second row.
func (c *Client) DisableRegion(ctx context.Context, regionID, scope string) error {
	var out struct {
		DisableRegion struct {
			Success bool `json:"success"`
		} `json:"DisableRegion"`
	}
	vars := map[string]any{"input": map[string]any{
		"regionId": regionID,
		"scope":    scope,
	}}
	if err := c.Execute(ctx, disableRegionMutation, vars, &out); err != nil {
		return err
	}
	if !out.DisableRegion.Success {
		return fmt.Errorf("DisableRegion reported failure for %s at %s", regionID, scope)
	}
	return nil
}

// EnableRegion stages an enable for regionID at scope.
//
// This is an override rather than a removal: the platform records an enabled
// state at the scope, which is what lets one account opt back in to a region
// disabled at its root. It carries the same silent success as DisableRegion
// when there is nothing to act on.
func (c *Client) EnableRegion(ctx context.Context, regionID, scope string) error {
	var out struct {
		EnableRegion struct {
			Success bool `json:"success"`
		} `json:"EnableRegion"`
	}
	vars := map[string]any{"input": map[string]any{
		"regionId": regionID,
		"scope":    scope,
	}}
	if err := c.Execute(ctx, enableRegionMutation, vars, &out); err != nil {
		return err
	}
	if !out.EnableRegion.Success {
		return fmt.Errorf("EnableRegion reported failure for %s at %s", regionID, scope)
	}
	return nil
}
