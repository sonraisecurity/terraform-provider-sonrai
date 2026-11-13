package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const regionsResponse = `{"data":{"RegionsControls":{"items":[
  {"regionId":"ap-south-1","regionName":"Asia Pacific (Mumbai)","cloudType":"aws",
   "criticalRegion":false,
   "status":{"status":"DISABLED","pending":true}},
  {"regionId":"us-east-1","regionName":"US East (N. Virginia)","cloudType":"aws",
   "criticalRegion":true,
   "status":{"status":"ENABLED","pending":false}},
  {"regionId":"eu-west-1","regionName":"Europe (Ireland)","cloudType":"aws",
   "criticalRegion":false,
   "status":{"status":"PARTIALLY_DISABLED","pending":false}}
]}}}`

func regionClient(t *testing.T, body string, captured *map[string]any) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
			*captured = req.Variables
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestRegionControlsReadsStatusAndFlags(t *testing.T) {
	regions, err := regionClient(t, regionsResponse, nil).RegionControls(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("RegionControls: %v", err)
	}
	if len(regions) != 3 {
		t.Fatalf("got %d regions, want 3", len(regions))
	}
	if regions[0].RegionID != "ap-south-1" || regions[2].RegionID != "us-east-1" {
		t.Errorf("regions are not sorted by id: %q .. %q", regions[0].RegionID, regions[2].RegionID)
	}

	byID := map[string]RegionControl{}
	for _, region := range regions {
		byID[region.RegionID] = region
	}
	if !byID["us-east-1"].Critical {
		t.Error("us-east-1 should be critical")
	}
	if byID["ap-south-1"].Critical {
		t.Error("ap-south-1 should not be critical")
	}
}

// TestRegionControlStatusAndPending pins the per-region readings, including a
// container's PARTIALLY_DISABLED staying distinct from DISABLED: something
// beneath it is still enabled, and conflating them would overstate the control.
func TestRegionControlStatusAndPending(t *testing.T) {
	c := regionClient(t, regionsResponse, nil)

	disabled, err := c.RegionControl(context.Background(), "aws/r-abcd", "ap-south-1")
	if err != nil {
		t.Fatalf("RegionControl: %v", err)
	}
	if disabled.Status != RegionDisabled {
		t.Errorf("ap-south-1 status = %q, want %q", disabled.Status, RegionDisabled)
	}
	if !disabled.Pending {
		t.Error("ap-south-1 has a staged change and should report pending")
	}

	partial, err := c.RegionControl(context.Background(), "aws/r-abcd", "eu-west-1")
	if err != nil {
		t.Fatalf("RegionControl: %v", err)
	}
	if partial.Status != RegionPartiallyDisabled {
		t.Errorf("eu-west-1 status = %q, want %q", partial.Status, RegionPartiallyDisabled)
	}
}

// TestRegionControlNotFound: a region the account does not have is absent from
// the platform's view entirely, which is what tells the provider there is
// nothing to control rather than something to disable.
func TestRegionControlNotFound(t *testing.T) {
	c := regionClient(t, regionsResponse, nil)
	_, err := c.RegionControl(context.Background(), "aws/r-abcd", "me-central-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// TestRegionControlsSelectsNoInheritanceFields: nothing consumes them, and a
// deployed server that predates them fails the whole query when one is
// selected.
func TestRegionControlsSelectsNoInheritanceFields(t *testing.T) {
	if strings.Contains(regionControlsQuery, "nherited") {
		t.Error("the regions query must not select inheritance fields")
	}
}

// TestRegionControlsUsesCapitalScopeFilter pins the filter spelling. The field
// is Scope on RegionControlFilter; lowercase is rejected by the schema, and
// the failure would only show against a real organization.
func TestRegionControlsUsesCapitalScopeFilter(t *testing.T) {
	if !strings.Contains(regionControlsQuery, "Scope: {op: EQ") {
		t.Error("the region scope filter must be spelled Scope, not scope")
	}
}

// TestRegionMutationsNeverSendApplyAtScope: region controls do not use the
// field, the enable path rejects it server-side, and servers that predate it
// reject it as an unknown input key. Neither mutation may carry it.
func TestRegionMutationsNeverSendApplyAtScope(t *testing.T) {
	var captured map[string]any
	c := regionClient(t, `{"data":{"DisableRegion":{"success":true}}}`, &captured)
	if err := c.DisableRegion(context.Background(), "ap-south-1", "aws/r-abcd"); err != nil {
		t.Fatalf("DisableRegion: %v", err)
	}
	input, _ := captured["input"].(map[string]any)
	if input["regionId"] != "ap-south-1" || input["scope"] != "aws/r-abcd" {
		t.Errorf("input = %#v", input)
	}
	if _, present := input["applyAtScope"]; present {
		t.Error("applyAtScope must not be sent to DisableRegion")
	}

	c = regionClient(t, `{"data":{"EnableRegion":{"success":true}}}`, &captured)
	if err := c.EnableRegion(context.Background(), "ap-south-1", "aws/r-abcd/123456789012"); err != nil {
		t.Fatalf("EnableRegion: %v", err)
	}
	input, _ = captured["input"].(map[string]any)
	if _, present := input["applyAtScope"]; present {
		t.Error("applyAtScope must not be sent to EnableRegion")
	}
}

func TestRegionMutationFailureIsAnError(t *testing.T) {
	c := regionClient(t, `{"data":{"DisableRegion":{"success":false}}}`, nil)
	if err := c.DisableRegion(context.Background(), "ap-south-1", "aws/r-abcd"); err == nil {
		t.Error("success:false must be reported as an error")
	}
	c = regionClient(t, `{"data":{"EnableRegion":{"success":false}}}`, nil)
	if err := c.EnableRegion(context.Background(), "ap-south-1", "aws/r-abcd"); err == nil {
		t.Error("success:false must be reported as an error")
	}
}
