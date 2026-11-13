package client

import (
	"context"
	"strings"
)

const pondRequestsQuery = `
query pondRequests($scope: String!) {
  PONDRequests(where: {
    scope: {op: EQ, value: $scope}
    status: {op: EQ, value: "APPROVED"}
  }) {
    items {
      controlId
      identity
      isSSO
      revokeAt
    }
  }
}`

// TimeboxedExemptions is the set of identities whose exemption from one
// control came from a Permissions on Demand grant that expires.
type TimeboxedExemptions struct {
	Identities  []string
	SSOActorIDs []string
}

// Has reports whether anything is timeboxed.
func (t *TimeboxedExemptions) Has() bool {
	return t != nil && (len(t.Identities) > 0 || len(t.SSOActorIDs) > 0)
}

// TimeboxedExemptions returns the identities exempt from controlID at scope by
// a time-limited Permissions on Demand grant.
//
// The exemption a PoD approval creates is an ordinary control exception with
// no expiry of its own: the time limit lives on the PoD request as revokeAt,
// and the platform revokes the request and removes the exception once it
// passes. Telling a timeboxed exemption from a permanent one therefore means
// reading the requests, not the exceptions.
//
// revokeAt is zero for a permanent grant. The API offers no filter for it, so
// the requests are narrowed by scope and status and sifted here.
func (c *Client) TimeboxedExemptions(ctx context.Context, scope, controlID string) (*TimeboxedExemptions, error) {
	var out struct {
		PONDRequests struct {
			Items []struct {
				ControlID string `json:"controlId"`
				Identity  string `json:"identity"`
				IsSSO     bool   `json:"isSSO"`
				RevokeAt  int64  `json:"revokeAt"`
			} `json:"items"`
		} `json:"PONDRequests"`
	}
	if err := c.Execute(ctx, pondRequestsQuery, map[string]any{"scope": scope}, &out); err != nil {
		return nil, err
	}

	identities := stringSet{}
	ssoActors := stringSet{}
	for _, item := range out.PONDRequests.Items {
		if item.RevokeAt <= 0 || item.Identity == "" {
			continue
		}
		if controlID != "" && !strings.EqualFold(item.ControlID, controlID) {
			continue
		}
		if item.IsSSO {
			ssoActors.add(item.Identity)
			continue
		}
		identities.add(item.Identity)
	}

	return &TimeboxedExemptions{
		Identities:  identities.sorted(),
		SSOActorIDs: ssoActors.sorted(),
	}, nil
}
