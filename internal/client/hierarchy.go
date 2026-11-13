package client

import (
	"context"
	"fmt"
	"strings"
)

// HierarchyEntry is one node of the cloud hierarchy: an account, project,
// subscription, OU, folder or the root itself.
type HierarchyEntry struct {
	// Scope is the canonical path, which is what every control mutation keys
	// on. It is not derivable from the account id: an account may sit directly
	// under the root, or nested one or more levels deep.
	Scope       string `json:"scope"`
	Name        string `json:"name"`
	ResourceID  string `json:"resourceId"`
	EntryType   string `json:"entryType"`
	CloudType   string `json:"cloudType"`
	ParentScope string `json:"parentScope"`
	// PurpleEnabled reports whether the account is enrolled in Cloud
	// Permissions Firewall. An account can exist in the hierarchy without being
	// enrolled, and control queries against it fail with "invalid scope".
	PurpleEnabled bool `json:"purpleEnabled"`
	// Active reports whether the account is still live in the cloud.
	Active bool `json:"active"`
}

// Leaf entry types, which differ per cloud. A leaf is a billable account,
// project or subscription -- the level at which the platform materializes
// control mappings.
const (
	EntryTypeAccount           = "account"
	EntryTypeManagementAccount = "managementAccount"
	EntryTypeProject           = "project"
	EntryTypeSubscription      = "subscription"
)

// IsLeafAccountType reports whether entryType names an account, project or
// subscription rather than a container such as an OU, folder or root.
func IsLeafAccountType(entryType string) bool {
	switch entryType {
	case EntryTypeAccount, EntryTypeManagementAccount, EntryTypeProject, EntryTypeSubscription:
		return true
	default:
		return false
	}
}

// DefaultLeafEntryType returns the hierarchy entry type naming a billable leaf
// for the cloud that scope belongs to.
func DefaultLeafEntryType(scope string) string {
	switch {
	case strings.HasPrefix(scope, "gcp/"):
		return EntryTypeProject
	case strings.HasPrefix(scope, "azure/"):
		return EntryTypeSubscription
	default:
		return EntryTypeAccount
	}
}

// CandidateResourceIDs returns the forms the hierarchy might store resourceID
// under, most literal first.
//
// AWS keeps bare ids for both accounts and OUs, but GCP stores leaves as
// "projects|my-project" and Azure as "|subscriptions|<guid>". A caller giving
// the id they see in their own cloud console should not have to know that, and
// an id already in qualified form must not be prefixed twice.
func CandidateResourceIDs(rootScope, resourceID string) []string {
	candidates := []string{resourceID}
	if strings.Contains(resourceID, "|") {
		return candidates
	}
	switch {
	case strings.HasPrefix(rootScope, "gcp/"):
		candidates = append(candidates, "projects|"+resourceID, "folders|"+resourceID)
	case strings.HasPrefix(rootScope, "azure/"):
		candidates = append(candidates, "|subscriptions|"+resourceID)
	}
	return candidates
}

// rootScope is matched with EQ: the column holds exactly the two-segment
// organization root on every row, and STARTS_WITH compiles to an unanchored
// LIKE, so "aws/r-abcd" would also match a sibling org "aws/r-abcd2".
const hierarchyQuery = `
query cloudHierarchy($rootScope: String!, $resourceIds: [String!]) {
  CloudHierarchyList(where: {
    rootScope: {op: EQ, value: $rootScope}
    resourceId: {op: IN_LIST, values: $resourceIds}
  }) {
    items {
      scope
      name
      resourceId
      entryType
      cloudType
      parentScope
      purpleEnabled
      active
    }
  }
}`

// ResolveScope finds the canonical scope path for a cloud-native resource id
// under rootScope. The id may name an account, project, subscription, OU,
// folder, management group, or the root itself.
//
// Scope paths are positional and change whenever something is reparented;
// resource ids do not. Since the platform rewrites every stored scope when an
// account or OU moves, addressing by id and resolving here is what keeps a
// configuration correct across a reorganization.
//
// entryType narrows the search when an id could name more than one kind of
// thing. Leave it empty to match any.
func (c *Client) ResolveScope(ctx context.Context, rootScope, resourceID, entryType string) (*HierarchyEntry, error) {
	var out struct {
		CloudHierarchyList struct {
			Items []HierarchyEntry `json:"items"`
		} `json:"CloudHierarchyList"`
	}
	vars := map[string]any{
		"rootScope":   rootScope,
		"resourceIds": CandidateResourceIDs(rootScope, resourceID),
	}
	if err := c.Execute(ctx, hierarchyQuery, vars, &out); err != nil {
		return nil, err
	}

	items := out.CloudHierarchyList.Items
	if entryType != "" {
		filtered := items[:0]
		for _, item := range items {
			if item.EntryType == entryType {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	switch len(items) {
	case 0:
		kind := "entry"
		if entryType != "" {
			kind = entryType
		}
		return nil, fmt.Errorf("%w: no %s with id %q under %q", ErrNotFound, kind, resourceID, rootScope)
	case 1:
		entry := items[0]
		return &entry, nil
	default:
		scopes := make([]string, 0, len(items))
		for _, item := range items {
			scopes = append(scopes, item.Scope)
		}
		return nil, fmt.Errorf("id %q matches %d entries under %q (%s); narrow root_scope",
			resourceID, len(items), rootScope, strings.Join(scopes, ", "))
	}
}
