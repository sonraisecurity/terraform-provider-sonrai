package client

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// errIncompleteBatch marks a per-scope read that answered, but not with
// something a missing entry can be trusted from.
var errIncompleteBatch = errors.New("per-scope result was incomplete or not keyed by control")

// batchFailure decides what a failed per-scope read means.
//
// Failures that are the caller's -- cancellation, or a token the platform
// rejects -- are returned, because the per-control reads would fail the same
// way. Anything else is answered with a nil result and no error, which is
// cached: every lookup at that scope then falls back to the per-control
// queries for the rest of the run, rather than retrying a batch read that
// already failed once for each control there.
func batchFailure(ctx context.Context, kind, scope string, err error) (any, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnauthorized) {
		return nil, err
	}
	tflog.Warn(ctx, "per-scope read unavailable, reading one control at a time", map[string]any{
		"kind":  kind,
		"scope": scope,
		"error": err.Error(),
	})
	return nil, nil
}

// scopeServiceStatusesQuery is the console's service list for one scope:
// every service there, with its control status.
const scopeServiceStatusesQuery = `
query scopeServiceStatuses($scope: String!) {
  CloudServices(where: {scope: {op: EQ, value: $scope}}) {
    items {
      controlKey
      status { status isPending }
    }
  }
}`

// scopeServiceStatuses returns the status of every service at scope, keyed by
// control key, or nil with no error when the list cannot be used and the
// caller should read one service at a time.
//
// The list need not be complete: a key missing from it is read on its own, so
// a short page costs a query rather than a wrong answer. What it must be is
// an account's list. At a container CloudServices aggregates, reporting
// statuses such as "Partially Protected" that the per-service query never
// returns, so a list containing one is discarded.
func (c *Client) scopeServiceStatuses(ctx context.Context, scope string) (map[string]ControlStatus, error) {
	if !c.cache.enabled() {
		return nil, nil
	}
	cached, err := c.cache.get(ctx, cacheKey("services", scope), func(ctx context.Context) (any, error) {
		var out struct {
			CloudServices struct {
				Items []struct {
					ControlKey string        `json:"controlKey"`
					Status     ControlStatus `json:"status"`
				} `json:"items"`
			} `json:"CloudServices"`
		}
		if err := c.Execute(ctx, scopeServiceStatusesQuery, map[string]any{"scope": scope}, &out); err != nil {
			return batchFailure(ctx, "services", scope, err)
		}

		statuses := make(map[string]ControlStatus, len(out.CloudServices.Items))
		for _, item := range out.CloudServices.Items {
			if item.ControlKey == "" || strings.HasPrefix(item.Status.Status, StatusPartialPrefix) {
				return batchFailure(ctx, "services", scope, errIncompleteBatch)
			}
			// Two rows disagreeing about one service leave no way to pick.
			if prior, seen := statuses[item.ControlKey]; seen && prior != item.Status {
				return batchFailure(ctx, "services", scope, errIncompleteBatch)
			}
			statuses[item.ControlKey] = item.Status
		}
		return statuses, nil
	})
	if err != nil {
		return nil, err
	}
	statuses, _ := cached.(map[string]ControlStatus)
	return statuses, nil
}
