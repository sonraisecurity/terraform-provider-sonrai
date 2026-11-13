package client

import (
	"context"
	"fmt"
)

const resetCloudRestrictionsMutation = `
mutation resetCloudRestrictions($rootScope: String!) {
  ResetCloudRestrictions(input: {rootScope: $rootScope}) {
    success
    transactionId
  }
}`

// ResetCloudRestrictions removes every control under rootScope and returns the
// id of the transaction doing it.
//
// This is destructive and organization-wide: it deletes staged control
// mappings outright, tears down applied controls, stages quarantined
// identities for removal, and re-stages JIT permission sets and levels for
// re-creation. It does not discriminate between controls Terraform manages and
// controls created elsewhere.
func (c *Client) ResetCloudRestrictions(ctx context.Context, rootScope string) (string, error) {
	var out struct {
		ResetCloudRestrictions struct {
			Success       bool   `json:"success"`
			TransactionID string `json:"transactionId"`
		} `json:"ResetCloudRestrictions"`
	}
	if err := c.ExecuteOnce(ctx, resetCloudRestrictionsMutation, map[string]any{"rootScope": rootScope}, &out); err != nil {
		return "", err
	}
	if !out.ResetCloudRestrictions.Success {
		return "", fmt.Errorf("ResetCloudRestrictions reported failure for %s", rootScope)
	}
	if out.ResetCloudRestrictions.TransactionID == "" {
		return "", fmt.Errorf("ResetCloudRestrictions returned no transaction id for %s", rootScope)
	}
	return out.ResetCloudRestrictions.TransactionID, nil
}
