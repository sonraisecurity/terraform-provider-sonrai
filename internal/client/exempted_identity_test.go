package client

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestValidateExemptedIdentityRefusesCatchAlls pins the guard that stands
// between a truncated ARN and an organization-wide exemption. The platform
// accepts these, so nothing else catches them.
func TestValidateExemptedIdentityRefusesCatchAlls(t *testing.T) {
	refused := []string{
		"",
		"   ",
		"arn:aws:iam::",
		"arn:aws:iam::123456789012:",
		"arn:aws:iam::123456789012:role/",
		"arn:aws:iam::123456789012:   ",
	}
	for _, identity := range refused {
		if err := ValidateExemptedIdentity(identity, false); err == nil {
			t.Errorf("ValidateExemptedIdentity(%q) = nil, want refusal", identity)
		}
	}
}

// TestValidateExemptedIdentityAllowsRealPatterns guards the other direction:
// short literal patterns are legitimate and in use, so the check must stay
// narrow enough not to reject them.
func TestValidateExemptedIdentityAllowsRealPatterns(t *testing.T) {
	allowed := []string{
		"arn:aws:iam::*:role/BreakGlass",
		"arn:aws:iam::123456789012:role/Admin",
		"arn:aws:iam::*:role/cpf-exempt-something",
		"*e/stacksets-exec-*",
		"sonrai",
	}
	for _, identity := range allowed {
		if err := ValidateExemptedIdentity(identity, false); err != nil {
			t.Errorf("ValidateExemptedIdentity(%q) = %v, want nil", identity, err)
		}
	}
}

// TestValidateExemptedIdentitySkipsSSO: an SSO actor id is matched as a user,
// not as an ARN pattern, so the ARN rule does not apply to it.
func TestValidateExemptedIdentitySkipsSSO(t *testing.T) {
	if err := ValidateExemptedIdentity("someone@example.com", true); err != nil {
		t.Errorf("got %v, want nil", err)
	}
	if err := ValidateExemptedIdentity("", true); err == nil {
		t.Error("an empty SSO identity should still be refused")
	}
}

func TestExemptionInputSetsOnlyRequestedTypes(t *testing.T) {
	input, err := exemptionInput("aws/r-abcd", "arn:aws:iam::*:role/BreakGlass", false,
		[]string{ExemptServiceProtect, ExemptServiceBlock})
	if err != nil {
		t.Fatalf("exemptionInput: %v", err)
	}

	for _, name := range ExemptionTypes {
		want := name == ExemptServiceProtect || name == ExemptServiceBlock
		if got := input[name]; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if input["isSso"] != false {
		t.Error("isSso should be false")
	}
	// Unlisted types must be sent as false, not omitted: the mutation replaces
	// the whole set, and an omitted field would leave a type unchanged.
	if len(input) != len(ExemptionTypes)+3 {
		t.Errorf("input has %d keys, want %d", len(input), len(ExemptionTypes)+3)
	}
}

func TestExemptionInputRejectsUnknownType(t *testing.T) {
	if _, err := exemptionInput("aws/r-abcd", "x", false, []string{"nonsense"}); err == nil {
		t.Error("expected an error for an unknown exemption type")
	}
}

// TestControlExemptionsTypesReadsExemptNotStaged: exempt carries the intended
// state and staged only says it is undeployed, so a type staged for REMOVAL
// (exempt false, staged true) must not read as still in force.
func TestControlExemptionsTypesReadsExemptNotStaged(t *testing.T) {
	c := controlExemptions{
		ServiceProtect: exemptionState{Exempt: true, Staged: false},
		ServiceBlock:   exemptionState{Exempt: true, Staged: true},
		Quarantine:     exemptionState{Exempt: false, Staged: true},
		RegionBlock:    exemptionState{},
	}
	got := c.types()
	want := []string{ExemptServiceBlock, ExemptServiceProtect}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("types() = %v, want %v", got, want)
	}
	if !c.anyPending() {
		t.Error("anyPending() = false, want true")
	}
}

func TestControlExemptionsNothingPending(t *testing.T) {
	c := controlExemptions{ServiceProtect: exemptionState{Exempt: true}}
	if c.anyPending() {
		t.Error("anyPending() = true, want false")
	}
}

func TestExemptedIdentityDeployedMatchesOnSSOFlag(t *testing.T) {
	c := clientReturning(t, `{"data":{"AppliedExemptedIdentities":{"items":[
		{"identity":"someone@example.com","isSso":true}]}}}`, nil)

	deployed, err := c.ExemptedIdentityDeployed(context.Background(), "aws/r-abcd", "someone@example.com", false)
	if err != nil {
		t.Fatalf("ExemptedIdentityDeployed: %v", err)
	}
	// The same string can exist as an SSO entry and a non-SSO pattern; only the
	// matching kind counts as deployed.
	if deployed {
		t.Error("non-SSO lookup matched an SSO row")
	}

	deployed, err = c.ExemptedIdentityDeployed(context.Background(), "aws/r-abcd", "someone@example.com", true)
	if err != nil {
		t.Fatalf("ExemptedIdentityDeployed: %v", err)
	}
	if !deployed {
		t.Error("SSO lookup did not match the SSO row")
	}
}

// TestUpdateExemptedIdentityAssertsEveryType: a type left out has to travel as
// false. That is the whole difference from staging, which only ever adds.
func TestUpdateExemptedIdentityAssertsEveryType(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"UpdateExemptedIdentity":{"success":true}}}`, &got)

	err := c.UpdateExemptedIdentity(context.Background(), "aws/r-abcd", "*e/BreakGlass", false,
		[]string{ExemptServiceBlock})
	if err != nil {
		t.Fatalf("UpdateExemptedIdentity: %v", err)
	}
	if !strings.Contains(got.Query, "UpdateExemptedIdentity") {
		t.Errorf("query did not call UpdateExemptedIdentity: %s", got.Query)
	}
	input, ok := got.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("input = %#v", got.Variables["input"])
	}
	for _, name := range ExemptionTypes {
		value, present := input[name]
		if !present {
			t.Errorf("%s was omitted", name)
		}
		if want := name == ExemptServiceBlock; value != want {
			t.Errorf("%s = %v, want %v", name, value, want)
		}
	}
	if _, present := input["action"]; present {
		t.Error("action is not part of UpdateExemptedIdentityInput")
	}
}

func TestUpdateExemptedIdentityRefusesAnEmptySet(t *testing.T) {
	c := clientReturning(t, `{"data":{"UpdateExemptedIdentity":{"success":true}}}`, nil)
	if err := c.UpdateExemptedIdentity(context.Background(), "aws/r-abcd", "*e/BreakGlass", false, nil); err == nil {
		t.Error("an empty set would stage every type for removal; expected an error")
	}
}

func TestUpdateExemptedIdentityReportsFailure(t *testing.T) {
	c := clientReturning(t, `{"data":{"UpdateExemptedIdentity":{"success":false}}}`, nil)
	err := c.UpdateExemptedIdentity(context.Background(), "aws/r-abcd", "*e/BreakGlass", false,
		[]string{ExemptServiceBlock})
	if err == nil {
		t.Error("success:false should be an error")
	}
}
