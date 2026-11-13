package client

import (
	"context"
	"fmt"
	"sort"
)

// AWS entitlement types a JIT enrolment can name.
const (
	EntitlementPermissionSet     = "AWS_PERMISSION_SET"
	EntitlementAccountAccessRole = "AWS_ACCOUNT_ACCESS_ROLE"
	EntitlementSAMLRole          = "AWS_SAML_ROLE"

	// entitlementTypeUnset is what rows written before account access role
	// support report. It means AWS_PERMISSION_SET and must never be sent back.
	entitlementTypeUnset = "NULL_AWS_FEDERATED_IDENTITY_TYPE"
)

// EntitlementTypes lists the types an enrolment may declare.
var EntitlementTypes = []string{
	EntitlementPermissionSet,
	EntitlementAccountAccessRole,
	EntitlementSAMLRole,
}

const setJitConfigurationMutation = `
mutation setJitConfiguration($input: SetJitConfigurationInput!) {
  SetJitConfiguration(input: $input) {
    success
    addedJitConfigurationIds
    removedJitConfigurationIds
  }
}`

const jitConfigurationQuery = `
query jitConfiguration($scope: String!) {
  JitConfiguration(where: {scope: {op: EQ, value: $scope}}) {
    items {
      scope
      autoPod
      denyFirst { isEnabled isInherited isPending }
      permissionSets {
        name
        type
        isFullAccess
        isInherited
        status { status isPending }
      }
    }
  }
}`

// JitEntitlement is one entitlement enrolled for JIT at a scope.
//
// Name is what both the read and the write use to identify it: the mutation
// takes the name in its id field, not the permission set's own id.
type JitEntitlement struct {
	Name       string
	Type       string
	FullAccess bool
}

// JitConfiguration is the JIT configuration defined at one scope.
//
// Only entries defined at the scope itself are reported. An enrolment
// inherited from an ancestor governs this scope but is not a record here, and
// re-submitting one as a direct enrolment would change its meaning.
type JitConfiguration struct {
	Scope        string
	DenyFirst    bool
	AutoPod      bool
	Entitlements []JitEntitlement

	// Pending is true while any part of the configuration has a staged change
	// that has not been submitted and deployed.
	Pending bool
}

// JitConfigurationChange is what one SetJitConfiguration call enrolled and
// unenrolled, by entitlement name.
type JitConfigurationChange struct {
	Added   []string
	Removed []string
}

// SetJitConfiguration replaces the whole JIT configuration at scope and
// reports what changed.
//
// The mutation is a replace, not a merge: entitlements left out are unenrolled.
// There is no per-entitlement mutation and no usable delete, so clearing a
// configuration means calling this with no entitlements and denyFirst false.
func (c *Client) SetJitConfiguration(ctx context.Context, scope string, denyFirst, autoPod bool, entitlements []JitEntitlement) (*JitConfigurationChange, error) {
	enrolled := make([]map[string]any, 0, len(entitlements))
	for _, e := range entitlements {
		if e.Name == "" {
			return nil, fmt.Errorf("entitlement name cannot be empty")
		}
		entry := map[string]any{
			"id":           e.Name,
			"isFullAccess": e.FullAccess,
		}
		// Sending the unset enum back would reject the row; omitting the field
		// means AWS_PERMISSION_SET, which is what it stands for.
		if e.Type != "" && e.Type != entitlementTypeUnset {
			entry["type"] = e.Type
		}
		enrolled = append(enrolled, entry)
	}

	var out struct {
		SetJitConfiguration struct {
			Success bool     `json:"success"`
			Added   []string `json:"addedJitConfigurationIds"`
			Removed []string `json:"removedJitConfigurationIds"`
		} `json:"SetJitConfiguration"`
	}
	vars := map[string]any{
		"input": map[string]any{
			"scope":                  scope,
			"isDenyFirst":            denyFirst,
			"autoPod":                autoPod,
			"enrolledPermissionSets": enrolled,
		},
	}
	if err := c.Execute(ctx, setJitConfigurationMutation, vars, &out); err != nil {
		return nil, err
	}
	if !out.SetJitConfiguration.Success {
		return nil, fmt.Errorf("SetJitConfiguration reported failure for %s", scope)
	}
	return &JitConfigurationChange{
		Added:   out.SetJitConfiguration.Added,
		Removed: out.SetJitConfiguration.Removed,
	}, nil
}

// JitConfiguration reads the configuration defined at exactly scope, or
// ErrNotFound when the scope has none of its own.
func (c *Client) JitConfiguration(ctx context.Context, scope string) (*JitConfiguration, error) {
	var out struct {
		JitConfiguration struct {
			Items []struct {
				Scope     string `json:"scope"`
				AutoPod   bool   `json:"autoPod"`
				DenyFirst struct {
					IsEnabled   bool `json:"isEnabled"`
					IsInherited bool `json:"isInherited"`
					IsPending   bool `json:"isPending"`
				} `json:"denyFirst"`
				PermissionSets []struct {
					Name         string `json:"name"`
					Type         string `json:"type"`
					IsFullAccess bool   `json:"isFullAccess"`
					IsInherited  bool   `json:"isInherited"`
					Status       struct {
						Status    string `json:"status"`
						IsPending bool   `json:"isPending"`
					} `json:"status"`
				} `json:"permissionSets"`
			} `json:"items"`
		} `json:"JitConfiguration"`
	}
	if err := c.Execute(ctx, jitConfigurationQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}
	if len(out.JitConfiguration.Items) == 0 {
		return nil, fmt.Errorf("%w: JIT configuration at scope %q", ErrNotFound, scope)
	}

	item := out.JitConfiguration.Items[0]
	cfg := &JitConfiguration{
		Scope:   item.Scope,
		AutoPod: item.AutoPod,
		Pending: item.DenyFirst.IsPending,
	}
	// An inherited deny-first is in force here but belongs to an ancestor.
	if !item.DenyFirst.IsInherited {
		cfg.DenyFirst = item.DenyFirst.IsEnabled
	}

	for _, ps := range item.PermissionSets {
		if ps.IsInherited || ps.Name == "" {
			continue
		}
		if ps.Status.IsPending {
			cfg.Pending = true
		}
		entitlementType := ps.Type
		if entitlementType == entitlementTypeUnset {
			entitlementType = EntitlementPermissionSet
		}
		cfg.Entitlements = append(cfg.Entitlements, JitEntitlement{
			Name:       ps.Name,
			Type:       entitlementType,
			FullAccess: ps.IsFullAccess,
		})
	}
	sort.Slice(cfg.Entitlements, func(i, j int) bool {
		return cfg.Entitlements[i].Name < cfg.Entitlements[j].Name
	})
	return cfg, nil
}

// userCountDisplay is deliberately not selected: deployed servers may predate
// the field, and its value is fully derivable from userCount (the count as a
// string, or N/A when absent), so querying it buys nothing but a validation
// error against an older org.
const awsFederatedIdentitiesQuery = `
query awsFederatedIdentities($scope: String!) {
  AWSFederatedIdentities(where: {scope: {op: EQ, value: $scope}}) {
    items {
      name
      type
      identityLabels
      userCount
    }
  }
}`

// Entitlement is an AWS entitlement federated users reach an account through,
// and so a candidate for JIT enrolment.
//
// Name is what an enrolment names it by. UserCount is a pointer because absent
// is not zero: a SAML role is assigned in the identity provider, so no access
// count applies to it, and reading that as zero would say "nobody uses this"
// about something that may be widely used.
type Entitlement struct {
	Name      string
	Type      string
	Labels    []string
	UserCount *int64
}

// Entitlements lists the AWS entitlements available at scope: Identity Center
// permission sets, account access roles and SAML roles.
//
// This reads AWSFederatedIdentities rather than PermissionSets. PermissionSets
// covers Identity Center permission sets, and it returns an absent userCount
// as zero, which erases the difference between "no one has access"
// and "an access count does not apply". The scope filter matches the scope and
// everything beneath it.
//
// An identity that qualifies as more than one type is reported as the more
// specific one, account access role ahead of SAML role.
func (c *Client) Entitlements(ctx context.Context, scope string) ([]Entitlement, error) {
	var out struct {
		AWSFederatedIdentities struct {
			Items []struct {
				Name           string   `json:"name"`
				Type           string   `json:"type"`
				IdentityLabels []string `json:"identityLabels"`
				UserCount      *int64   `json:"userCount"`
			} `json:"items"`
		} `json:"AWSFederatedIdentities"`
	}
	if err := c.Execute(ctx, awsFederatedIdentitiesQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}

	entitlements := make([]Entitlement, 0, len(out.AWSFederatedIdentities.Items))
	for _, item := range out.AWSFederatedIdentities.Items {
		if item.Name == "" {
			continue
		}
		entitlementType := item.Type
		if entitlementType == "" || entitlementType == entitlementTypeUnset {
			entitlementType = EntitlementPermissionSet
		}
		entitlements = append(entitlements, Entitlement{
			Name:      item.Name,
			Type:      entitlementType,
			Labels:    item.IdentityLabels,
			UserCount: item.UserCount,
		})
	}
	sort.Slice(entitlements, func(i, j int) bool {
		return entitlements[i].Name < entitlements[j].Name
	})
	return entitlements, nil
}
