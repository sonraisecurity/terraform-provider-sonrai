package client

import (
	"context"
	"fmt"
)

// PendingChange is one change waiting to be submitted under a root scope.
type PendingChange struct {
	ID         string   `json:"id"`
	ChangeType string   `json:"changeType"`
	Name       string   `json:"changeName"`
	Action     string   `json:"action"`
	Scope      string   `json:"scope"`
	ControlKey string   `json:"controlKey"`
	StagedBy   string   `json:"sonraiUser"`
	Identities []string `json:"identities"`
}

// Change types the platform queues itself. The API reports them under the
// caller's own user, so the type is the only thing that tells them apart from
// changes the caller staged.
var organizationWideChangeTypes = map[string]struct{}{
	"third_party_account":      {},
	"custom_control_sync":      {},
	"organization_policy_sync": {},
}

// OrganizationWide reports whether the platform queued this change for every
// submitter, rather than the caller having staged it: updates to known third
// party accounts, and edits to custom controls and organization policies that
// are already deployed.
func (p PendingChange) OrganizationWide() bool {
	_, ok := organizationWideChangeTypes[p.ChangeType]
	return ok
}

// The page cap exists only so a misbehaving API cannot make the loop run
// forever. Reaching it is an error: a truncated list would read as a complete
// one to whoever is deciding whether it is safe to submit.
const (
	pendingChangesPageSize = 500
	pendingChangesMaxPages = 40
)

const pendingChangesQuery = `
query pendingChanges($rootScope: String!, $limit: Long, $offset: Long) {
  PendingChanges(where: {rootScope: {op: EQ, value: $rootScope}, inTransaction: {op: EQ, value: false}}) {
    items(orderBy: {id: {order: ASC}}, limit: $limit, offset: $offset) {
      id
      changeType
      changeName
      action
      scope
      controlKey
      sonraiUser
      identities
    }
  }
}`

// PendingChanges lists what a submission under rootScope made with this token
// would deploy: the changes its identity has staged and not yet submitted, and
// the ones the platform has queued for the organization.
//
// Changes other users staged are not included. The platform scopes this query
// to the caller exactly as it scopes a submission, which is what makes the
// answer a faithful preview of one.
func (c *Client) PendingChanges(ctx context.Context, rootScope string) ([]PendingChange, error) {
	var all []PendingChange
	for page := 0; page < pendingChangesMaxPages; page++ {
		var out struct {
			PendingChanges struct {
				Items []PendingChange `json:"items"`
			} `json:"PendingChanges"`
		}
		vars := map[string]any{
			"rootScope": rootScope,
			"limit":     pendingChangesPageSize,
			"offset":    page * pendingChangesPageSize,
		}
		if err := c.Execute(ctx, pendingChangesQuery, vars, &out); err != nil {
			return nil, err
		}
		all = append(all, out.PendingChanges.Items...)
		if len(out.PendingChanges.Items) < pendingChangesPageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("more than %d changes are pending under %q; refusing to report a partial list",
		pendingChangesPageSize*pendingChangesMaxPages, rootScope)
}
