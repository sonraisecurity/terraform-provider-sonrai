package client

import (
	"context"
	"fmt"
)

// EntryTypeOrganization names the top of a cloud organization.
const EntryTypeOrganization = "organization"

// DeployAccount is the cloud account a root scope's CloudFormation template
// must be deployed into.
type DeployAccount struct {
	// AccountID is where the stack goes.
	AccountID string
	// Delegated reports whether AccountID came from a delegated admin
	// configuration rather than being the organization's management account.
	Delegated bool
	// ManagementAccountID is the organization's management account, which is
	// the deploy target only when Delegated is false.
	ManagementAccountID string
	// OrganizationID is the cloud-native organization id, e.g. "o-abc123".
	OrganizationID string
	// Onboarded reports whether the organization has completed onboarding.
	Onboarded bool
}

// rootScope is matched with EQ for the same reason as hierarchyQuery: an
// unanchored prefix match can pull in a sibling organization, and this query
// picks the account a CloudFormation stack is deployed into.
const hierarchyRootsQuery = `
query hierarchyRoots($rootScope: String!, $entryTypes: [String!]) {
  CloudHierarchyList(where: {
    rootScope: {op: EQ, value: $rootScope}
    entryType: {op: IN_LIST, values: $entryTypes}
  }) {
    items { resourceId entryType }
  }
}`

const onboardingMetadataQuery = `
query onboardingMetadata($resourceId: String) {
  OnboardingMetadata(where: {resourceId: {op: EQ, value: $resourceId}}) {
    items { resourceId organizationId onboarded delegatedAccountId }
  }
}`

// ResolveDeployAccount works out which account a root scope's CloudFormation
// template must be deployed into.
//
// It is not the management account by default. When the organization is
// onboarded with a delegated admin, the stack goes to that account instead, and
// the management account is never touched. Deploying to the wrong one does not
// fail cleanly -- it creates a second, unmanaged stack in the wrong place -- so
// this resolves it rather than letting a caller assume.
func (c *Client) ResolveDeployAccount(ctx context.Context, rootScope string) (*DeployAccount, error) {
	var roots struct {
		CloudHierarchyList struct {
			Items []struct {
				ResourceID string `json:"resourceId"`
				EntryType  string `json:"entryType"`
			} `json:"items"`
		} `json:"CloudHierarchyList"`
	}
	vars := map[string]any{
		"rootScope":  rootScope,
		"entryTypes": []string{EntryTypeOrganization, EntryTypeManagementAccount},
	}
	if err := c.Execute(ctx, hierarchyRootsQuery, vars, &roots); err != nil {
		return nil, err
	}

	var organizationResourceID, managementAccountID string
	for _, item := range roots.CloudHierarchyList.Items {
		switch item.EntryType {
		case EntryTypeOrganization:
			// A second match means the filter caught more than one organization.
			// Guessing between them picks a CloudFormation deploy target, and
			// deploying to the wrong account does not fail cleanly.
			if organizationResourceID != "" && organizationResourceID != item.ResourceID {
				return nil, fmt.Errorf("root scope %q matches more than one organization (%s, %s)",
					rootScope, organizationResourceID, item.ResourceID)
			}
			organizationResourceID = item.ResourceID
		case EntryTypeManagementAccount:
			if managementAccountID != "" && managementAccountID != item.ResourceID {
				return nil, fmt.Errorf("root scope %q matches more than one management account (%s, %s)",
					rootScope, managementAccountID, item.ResourceID)
			}
			managementAccountID = item.ResourceID
		}
	}
	if organizationResourceID == "" && managementAccountID == "" {
		return nil, fmt.Errorf("%w: no organization or management account under %q", ErrNotFound, rootScope)
	}

	result := &DeployAccount{
		AccountID:           managementAccountID,
		ManagementAccountID: managementAccountID,
	}

	// Without an organization entry there is nothing to look delegation up
	// against, so the management account is the only answer available.
	if organizationResourceID == "" {
		return result, nil
	}

	var metadata struct {
		OnboardingMetadata struct {
			Items []struct {
				OrganizationID     string `json:"organizationId"`
				Onboarded          bool   `json:"onboarded"`
				DelegatedAccountID string `json:"delegatedAccountId"`
			} `json:"items"`
		} `json:"OnboardingMetadata"`
	}
	err := c.Execute(ctx, onboardingMetadataQuery, map[string]any{"resourceId": organizationResourceID}, &metadata)
	if err != nil {
		return nil, err
	}

	if len(metadata.OnboardingMetadata.Items) > 0 {
		item := metadata.OnboardingMetadata.Items[0]
		result.OrganizationID = item.OrganizationID
		result.Onboarded = item.Onboarded
		if item.DelegatedAccountID != "" {
			result.AccountID = item.DelegatedAccountID
			result.Delegated = true
		}
	}

	if result.AccountID == "" {
		return nil, fmt.Errorf("%w: no deploy account for %q; it has neither a delegated admin nor a management account",
			ErrNotFound, rootScope)
	}
	return result, nil
}
