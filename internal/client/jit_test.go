package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
)

const jitConfigResponse = `{"data":{"JitConfiguration":{"items":[{
  "scope":"aws/r-abcd",
  "autoPod":true,
  "denyFirst":{"isEnabled":true,"isInherited":false,"isPending":false},
  "permissionSets":[
    {"name":"PowerUserAccess","type":"AWS_PERMISSION_SET","isFullAccess":false,
     "isInherited":false,"status":{"status":"ENROLLED","isPending":false}},
    {"name":"Inherited","type":"AWS_PERMISSION_SET","isFullAccess":true,
     "isInherited":true,"status":{"status":"ENROLLED","isPending":false}},
    {"name":"LegacyRow","type":"NULL_AWS_FEDERATED_IDENTITY_TYPE","isFullAccess":false,
     "isInherited":false,"status":{"status":"ENROLLED","isPending":false}},
    {"name":"Breakglass","type":"AWS_ACCOUNT_ACCESS_ROLE","isFullAccess":true,
     "isInherited":false,"status":{"status":"PENDING_ENROLLMENT","isPending":true}}
  ]}]}}}`

// jitClient answers every request with body, recording the request variables
// into vars when it is non-nil.
func jitClient(t *testing.T, body string, vars *map[string]any) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if vars != nil {
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
			*vars = req.Variables
		}
		_, _ = w.Write([]byte(body))
	})
}

// TestJitConfigurationExcludesInherited: an enrolment inherited from an
// ancestor governs this scope but is not a record here. Adopting one would put
// something in state that cannot be removed at this scope.
func TestJitConfigurationExcludesInherited(t *testing.T) {
	cfg, err := jitClient(t, jitConfigResponse, nil).JitConfiguration(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("JitConfiguration: %v", err)
	}

	names := make([]string, 0, len(cfg.Entitlements))
	for _, e := range cfg.Entitlements {
		names = append(names, e.Name)
	}
	want := []string{"Breakglass", "LegacyRow", "PowerUserAccess"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("entitlements = %v, want %v", names, want)
	}
}

// TestJitConfigurationNormalisesUnsetType: rows written before account access
// role support report the NULL enum, which stands for AWS_PERMISSION_SET.
func TestJitConfigurationNormalisesUnsetType(t *testing.T) {
	cfg, err := jitClient(t, jitConfigResponse, nil).JitConfiguration(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("JitConfiguration: %v", err)
	}
	for _, e := range cfg.Entitlements {
		if e.Name == "LegacyRow" && e.Type != EntitlementPermissionSet {
			t.Errorf("LegacyRow type = %q, want %q", e.Type, EntitlementPermissionSet)
		}
	}
}

// TestJitConfigurationPendingFromAnyPart: pending has to cover an entitlement
// mid-enrolment, not just deny-first.
func TestJitConfigurationPendingFromAnyPart(t *testing.T) {
	cfg, err := jitClient(t, jitConfigResponse, nil).JitConfiguration(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("JitConfiguration: %v", err)
	}
	if !cfg.Pending {
		t.Error("Pending = false, want true: Breakglass is PENDING_ENROLLMENT")
	}
	if !cfg.DenyFirst || !cfg.AutoPod {
		t.Errorf("DenyFirst = %v, AutoPod = %v, want both true", cfg.DenyFirst, cfg.AutoPod)
	}
}

// TestJitConfigurationInheritedDenyFirstNotAdopted: deny-first carries the same
// inheritance trap as the enrolments.
func TestJitConfigurationInheritedDenyFirstNotAdopted(t *testing.T) {
	c := jitClient(t, `{"data":{"JitConfiguration":{"items":[{
      "scope":"aws/r-abcd/123456789012","autoPod":false,
      "denyFirst":{"isEnabled":true,"isInherited":true,"isPending":false},
      "permissionSets":[]}]}}}`, nil)

	cfg, err := c.JitConfiguration(context.Background(), "aws/r-abcd/123456789012")
	if err != nil {
		t.Fatalf("JitConfiguration: %v", err)
	}
	if cfg.DenyFirst {
		t.Error("DenyFirst = true, want false: it is inherited, not set here")
	}
}

func TestJitConfigurationNotFound(t *testing.T) {
	c := jitClient(t, `{"data":{"JitConfiguration":{"items":[]}}}`, nil)

	if _, err := c.JitConfiguration(context.Background(), "aws/r-abcd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// TestSetJitConfigurationSendsNameAsID pins the mapping that is easy to get
// wrong: the mutation identifies an entitlement by name, and the unset type
// enum must be omitted rather than echoed back.
func TestSetJitConfigurationSendsNameAsID(t *testing.T) {
	var captured map[string]any
	c := jitClient(t, `{"data":{"SetJitConfiguration":{"success":true}}}`, &captured)

	_, err := c.SetJitConfiguration(context.Background(), "aws/r-abcd", true, false, []JitEntitlement{
		{Name: "PowerUserAccess", Type: EntitlementPermissionSet},
		{Name: "LegacyRow", Type: entitlementTypeUnset},
		{Name: "Breakglass", Type: EntitlementAccountAccessRole, FullAccess: true},
	})
	if err != nil {
		t.Fatalf("SetJitConfiguration: %v", err)
	}

	input, _ := captured["input"].(map[string]any)
	sets, _ := input["enrolledPermissionSets"].([]any)
	if len(sets) != 3 {
		t.Fatalf("sent %d entitlements, want 3", len(sets))
	}

	first, _ := sets[0].(map[string]any)
	if first["id"] != "PowerUserAccess" {
		t.Errorf("id = %v, want the NAME PowerUserAccess", first["id"])
	}

	legacy, _ := sets[1].(map[string]any)
	if _, present := legacy["type"]; present {
		t.Error("the unset type enum must be omitted, not echoed back")
	}

	breakglass, _ := sets[2].(map[string]any)
	if breakglass["type"] != EntitlementAccountAccessRole {
		t.Errorf("type = %v, want %q", breakglass["type"], EntitlementAccountAccessRole)
	}
	if breakglass["isFullAccess"] != true {
		t.Error("isFullAccess should survive to the wire")
	}

	if input["isDenyFirst"] != true {
		t.Error("isDenyFirst should be true")
	}
}

// TestSetJitConfigurationClearsWithEmptyList: there is no delete mutation, so
// an empty write is the removal path and must send an empty list rather than
// null.
func TestSetJitConfigurationClearsWithEmptyList(t *testing.T) {
	var captured map[string]any
	c := jitClient(t, `{"data":{"SetJitConfiguration":{"success":true}}}`, &captured)

	if _, err := c.SetJitConfiguration(context.Background(), "aws/r-abcd", false, false, nil); err != nil {
		t.Fatalf("SetJitConfiguration: %v", err)
	}

	input, _ := captured["input"].(map[string]any)
	sets, ok := input["enrolledPermissionSets"].([]any)
	if !ok {
		t.Fatalf("enrolledPermissionSets = %#v, want an empty list", input["enrolledPermissionSets"])
	}
	if len(sets) != 0 {
		t.Errorf("sent %d entitlements, want none", len(sets))
	}
}

func TestSetJitConfigurationRejectsEmptyName(t *testing.T) {
	c := jitClient(t, `{"data":{"SetJitConfiguration":{"success":true}}}`, nil)
	_, err := c.SetJitConfiguration(context.Background(), "aws/r-abcd", false, false,
		[]JitEntitlement{{Name: ""}})
	if err == nil {
		t.Error("expected an error for an empty entitlement name")
	}
}

func TestSetJitConfigurationFailureIsAnError(t *testing.T) {
	c := jitClient(t, `{"data":{"SetJitConfiguration":{"success":false}}}`, nil)
	if _, err := c.SetJitConfiguration(context.Background(), "aws/r-abcd", false, false, nil); err == nil {
		t.Error("success:false must be reported as an error")
	}
}

// TestEntitlementsKeepsAbsentUserCountAbsent: a SAML role is assigned in the
// identity provider, so no access count applies to it. Reporting that as zero
// would read as "nobody uses this" about something that may be widely used --
// which is exactly why this reads AWSFederatedIdentities and not PermissionSets,
// which returns an absent count as zero.
func TestEntitlementsKeepsAbsentUserCountAbsent(t *testing.T) {
	c := jitClient(t, `{"data":{"AWSFederatedIdentities":{"items":[
      {"name":"PowerUserAccess","type":"AWS_PERMISSION_SET","identityLabels":["eng"],
       "userCount":42},
      {"name":"SamlAdmins","type":"AWS_SAML_ROLE","identityLabels":[],
       "userCount":null},
      {"name":"LegacyRow","type":"NULL_AWS_FEDERATED_IDENTITY_TYPE","identityLabels":[],
       "userCount":0}
    ]}}}`, nil)

	got, err := c.Entitlements(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("Entitlements: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entitlements, want 3", len(got))
	}

	byName := map[string]Entitlement{}
	for _, e := range got {
		byName[e.Name] = e
	}

	if saml := byName["SamlAdmins"]; saml.UserCount != nil {
		t.Errorf("SamlAdmins UserCount = %v, want nil: no count applies", *saml.UserCount)
	}

	// Zero has to stay distinguishable from absent.
	legacy := byName["LegacyRow"]
	if legacy.UserCount == nil || *legacy.UserCount != 0 {
		t.Error("an explicit zero must survive as zero, not become absent")
	}
	if legacy.Type != EntitlementPermissionSet {
		t.Errorf("LegacyRow type = %q, want the unset enum normalised", legacy.Type)
	}

	if power := byName["PowerUserAccess"]; power.UserCount == nil || *power.UserCount != 42 {
		t.Error("PowerUserAccess should report 42 users")
	}
}
