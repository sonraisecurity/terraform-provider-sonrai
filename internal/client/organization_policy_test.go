package client

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const testPolicyID = "a1b2c3d4-0000-4000-8000-000000000000"

const testPolicyDocument = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:DeleteBucket"],"Resource":"*"}]}`

func TestCreateOrganizationPolicySendsTheDefinition(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"CreateOrganizationPolicy":{"success":true,"policy":{
		"id":"`+testPolicyID+`","policyName":"deny-delete","policyType":"SERVICE_CONTROL_POLICY",
		"currentContent":"{}","source":"authored","state":"ORGANIZATION_POLICY_MANAGED"}}}}`, &got)

	created, err := c.CreateOrganizationPolicy(context.Background(), NewOrganizationPolicy{
		Name: "deny-delete", Description: "d", Type: ServiceControlPolicy,
		Content: testPolicyDocument, Scope: "aws/r-abcd",
	})
	if err != nil {
		t.Fatalf("CreateOrganizationPolicy: %v", err)
	}
	if created.ID != testPolicyID || !created.Authored || !created.Managed {
		t.Errorf("created = %+v", created)
	}
	input, _ := got.Variables["input"].(map[string]any)
	want := map[string]any{
		"policyName": "deny-delete", "policyDescription": "d", "policyType": ServiceControlPolicy,
		"content": testPolicyDocument, "rootScope": "aws/r-abcd",
	}
	if !reflect.DeepEqual(input, want) {
		t.Errorf("input = %v, want %v", input, want)
	}
}

// TestCreateOrganizationPolicyRefusesBeforeCalling: the platform answers a
// name or type it will not take with an error that gives no reason, so the
// ones that can be known in advance are refused here with one.
func TestCreateOrganizationPolicyRefusesBeforeCalling(t *testing.T) {
	c := clientReturning(t, `{"data":{}}`, nil)
	cases := map[string]NewOrganizationPolicy{
		"reserved":         {Name: "SonraiSCP", Type: ServiceControlPolicy, Content: testPolicyDocument, Scope: "aws/r-abcd"},
		"needs a name":     {Name: "  ", Type: ServiceControlPolicy, Content: testPolicyDocument, Scope: "aws/r-abcd"},
		"unknown":          {Name: "p", Type: "TAG_POLICY", Content: testPolicyDocument, Scope: "aws/r-abcd"},
		"policy document":  {Name: "p", Type: ServiceControlPolicy, Content: " ", Scope: "aws/r-abcd"},
		"needs the root s": {Name: "p", Type: ServiceControlPolicy, Content: testPolicyDocument},
	}
	for want, policy := range cases {
		_, err := c.CreateOrganizationPolicy(context.Background(), policy)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: err = %v, want one mentioning %q", policy, err, want)
		}
	}
}

func TestCreateOrganizationPolicyIsNotRetried(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := c.CreateOrganizationPolicy(context.Background(), NewOrganizationPolicy{
		Name: "p", Type: ServiceControlPolicy, Content: testPolicyDocument, Scope: "aws/r-abcd",
	})
	// A retry after a lost success would leave two policies with one name.
	if err == nil || calls != 1 {
		t.Errorf("err = %v after %d calls, want one failed call", err, calls)
	}
}

func TestOrganizationPolicyReadsMappings(t *testing.T) {
	c := clientReturning(t, `{"data":{"GetOrganizationPolicy":{
		"policy":{"id":"`+testPolicyID+`","policyName":"p","policyDescription":"d",
			"policyType":"RESOURCE_CONTROL_POLICY","currentContent":"{}","source":"authored",
			"state":"ORGANIZATION_POLICY_MANAGED"},
		"stagedMappings":[{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/ou-1/222","action":"ADD"},
			{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/ou-2/333","action":"REMOVE"}],
		"appliedMappings":[{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/ou-2"},
			{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/ou-2/333"},
			{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/ou-2/444"}]}}}`, nil)

	detail, err := c.OrganizationPolicy(context.Background(), testPolicyID)
	if err != nil {
		t.Fatalf("OrganizationPolicy: %v", err)
	}
	if detail.Policy.Type != ResourceControlPolicy || !detail.Deployed() {
		t.Errorf("detail = %+v", detail)
	}
	// Rows exist at every scope beneath an attachment; only the top one is
	// where the policy is attached.
	if got := detail.DeployedScopes(); !reflect.DeepEqual(got, []string{"aws/r-abcd/ou-2"}) {
		t.Errorf("DeployedScopes = %v", got)
	}
	if got := detail.DeployedRootScopes(); !reflect.DeepEqual(got, []string{"aws/r-abcd"}) {
		t.Errorf("DeployedRootScopes = %v", got)
	}
	attached := map[string]bool{
		"aws/r-abcd/ou-1/222": true,  // staged to be attached
		"aws/r-abcd/ou-2":     true,  // deployed
		"aws/r-abcd/ou-2/333": false, // deployed, removal staged
		"aws/r-abcd/ou-2/444": true,  // deployed beneath the attachment
		"aws/r-abcd/ou-3":     false,
	}
	for scope, want := range attached {
		if got := detail.AttachedAt(scope); got != want {
			t.Errorf("AttachedAt(%s) = %v, want %v", scope, got, want)
		}
	}
	// A staged attachment is not in force, and a staged removal still is.
	deployed := map[string]bool{
		"aws/r-abcd/ou-1/222": false,
		"aws/r-abcd/ou-2":     true,
		"aws/r-abcd/ou-2/333": true,
		"aws/r-abcd/ou-3":     false,
	}
	for scope, want := range deployed {
		if got := detail.DeployedAt(scope); got != want {
			t.Errorf("DeployedAt(%s) = %v, want %v", scope, got, want)
		}
	}
}

func TestOrganizationPolicyNotFound(t *testing.T) {
	c := clientReturning(t, `{"errors":[{"message":"NOT_FOUND: GetOrganizationPolicy: managed policy not found"}]}`, nil)
	if _, err := c.OrganizationPolicy(context.Background(), testPolicyID); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTopmostScopesDoesNotConfuseSiblingPrefixes(t *testing.T) {
	got := topmostScopes([]string{"aws/r-abcd/ou-1/111", "aws/r-abcd/ou-1", "aws/r-abcd/ou-10", "aws/r-abcd/ou-1"})
	want := []string{"aws/r-abcd/ou-1", "aws/r-abcd/ou-10"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topmostScopes = %v, want %v", got, want)
	}
}

func TestUpdateOrganizationPolicyReplacesTheDocument(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"UpdateOrganizationPolicy":{"success":true}}}`, &got)
	err := c.UpdateOrganizationPolicy(context.Background(), testPolicyID, NewOrganizationPolicy{
		Name: "renamed", Description: "d2", Type: ServiceControlPolicy, Content: testPolicyDocument,
	})
	if err != nil {
		t.Fatalf("UpdateOrganizationPolicy: %v", err)
	}
	input, _ := got.Variables["input"].(map[string]any)
	want := map[string]any{
		"id": testPolicyID, "policyName": "renamed", "policyDescription": "d2", "policyJson": testPolicyDocument,
	}
	if !reflect.DeepEqual(input, want) {
		t.Errorf("input = %v, want %v", input, want)
	}
}

// TestUnmanageReportsARefusalAsAnError: the platform answers a policy that is
// still deployed with success false and no GraphQL error, which would read as
// a successful delete.
func TestUnmanageReportsARefusalAsAnError(t *testing.T) {
	c := clientReturning(t, `{"data":{"UnmanageOrganizationPolicy":{"success":false,
		"message":"cannot stop managing \"p\" while it is still attached at 2 scope(s)",
		"attachments":[{"rootScope":"aws/r-abcd","scopes":["aws/r-abcd/ou-1","aws/r-abcd/ou-1/111"]}]}}}`, nil)

	err := c.UnmanageOrganizationPolicy(context.Background(), testPolicyID)
	var live *PolicyStillLiveError
	if !errors.As(err, &live) {
		t.Fatalf("err = %v, want a *PolicyStillLiveError", err)
	}
	if !reflect.DeepEqual(live.Scopes, []string{"aws/r-abcd/ou-1"}) || !strings.Contains(live.Error(), "still attached") {
		t.Errorf("live = %+v", live)
	}
}

func TestAttachAndDetachApplyAtTheScopeItself(t *testing.T) {
	for operation, call := range map[string]func(*Client) error{
		"AttachOrganizationPolicy": func(c *Client) error {
			return c.AttachOrganizationPolicy(context.Background(), testPolicyID, "aws/r-abcd/ou-1")
		},
		"DetachOrganizationPolicy": func(c *Client) error {
			return c.DetachOrganizationPolicy(context.Background(), testPolicyID, "aws/r-abcd/ou-1")
		},
	} {
		var got capture
		c := clientReturning(t, `{"data":{"`+operation+`":{"success":true}}}`, &got)
		if err := call(c); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if !strings.Contains(got.Query, operation) {
			t.Errorf("%s: query = %s", operation, got.Query)
		}
		input, _ := got.Variables["input"].(map[string]any)
		// Without applyAtScope the platform attaches to each account beneath
		// separately, and accounts added later are missed.
		if input["applyAtScope"] != true || input["id"] != testPolicyID || input["scope"] != "aws/r-abcd/ou-1" {
			t.Errorf("%s: input = %v", operation, input)
		}

		failing := clientReturning(t, `{"data":{"`+operation+`":{"success":false}}}`, nil)
		if err := call(failing); err == nil {
			t.Errorf("%s: success false should be an error", operation)
		}
	}
}

func TestReviewOrganizationPolicySeparatesBlockingFindings(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"ReviewOrganizationPolicy":{"validationFindings":[
		{"findingDetails":"The action s3:Nope does not exist.","findingType":"FINDING_ERROR","issueCode":"INVALID_ACTION"},
		{"findingDetails":"Consider a condition.","findingType":"FINDING_SUGGESTION","issueCode":"X"}],
		"sizeCharacters":100,"sizeLimitCharacters":5120}}}`, &got)

	review, err := c.ReviewOrganizationPolicy(context.Background(), ResourceControlPolicy, testPolicyDocument, "aws/r-abcd")
	if err != nil {
		t.Fatalf("ReviewOrganizationPolicy: %v", err)
	}
	if len(review.Findings) != 2 || !review.Findings[0].Blocking() || review.Findings[1].Blocking() {
		t.Errorf("findings = %+v", review.Findings)
	}
	if review.Size != 100 || review.SizeLimit != 5120 {
		t.Errorf("size = %d of %d", review.Size, review.SizeLimit)
	}
	input, _ := got.Variables["input"].(map[string]any)
	if scopes, _ := input["targetScopes"].([]any); len(scopes) != 1 || scopes[0] != "aws/r-abcd" {
		t.Errorf("targetScopes = %v", input["targetScopes"])
	}
}

func TestOrganizationPoliciesListsManagedAndDiscovered(t *testing.T) {
	c := clientReturning(t, `{"data":{"ListOrganizationPolicies":{"items":[
		{"id":"2","policyName":"zeta","policyType":"SERVICE_CONTROL_POLICY","source":"discovered",
			"state":"ORGANIZATION_POLICY_UNMANAGED","attachedTargets":["aws/r-abcd/ou-1"]},
		{"id":"1","policyName":"alpha","policyType":"SERVICE_CONTROL_POLICY","source":"authored",
			"state":"ORGANIZATION_POLICY_MANAGED",
			"appliedMappings":[{"scope":"aws/r-abcd/ou-2/111"},{"scope":"aws/r-abcd/ou-2"}]},
		{"id":"3","policyName":"alpha","policyType":"SERVICE_CONTROL_POLICY","source":"discovered",
			"state":"ORGANIZATION_POLICY_UNMANAGED"}]}}}`, nil)

	policies, err := c.OrganizationPolicies(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("OrganizationPolicies: %v", err)
	}
	if len(policies) != 3 || policies[0].ID != "1" || policies[1].ID != "3" || policies[2].ID != "2" {
		t.Fatalf("policies = %+v", policies)
	}
	if !policies[0].Authored || !policies[0].Managed || !reflect.DeepEqual(policies[0].AttachedScopes, []string{"aws/r-abcd/ou-2"}) {
		t.Errorf("authored policy = %+v", policies[0])
	}
	if policies[2].Managed || !reflect.DeepEqual(policies[2].AttachedScopes, []string{"aws/r-abcd/ou-1"}) {
		t.Errorf("discovered policy = %+v", policies[2])
	}

	// Only a managed policy takes a name: a discovered one of the same name
	// is AWS's, and no obstacle to writing one in Sonrai.
	named, err := c.ManagedOrganizationPoliciesNamed(context.Background(), "aws/r-abcd", "alpha")
	if err != nil || len(named) != 1 || named[0].ID != "1" {
		t.Errorf("named = %+v, err = %v", named, err)
	}

	if _, err := c.OrganizationPolicies(context.Background(), "gcp/organizations|1"); err == nil {
		t.Error("a GCP root scope should be refused")
	}
}

func TestOrganizationPolicySyncPendingMatchesThePolicy(t *testing.T) {
	c := clientReturning(t, `{"data":{"PendingChanges":{"items":[
		{"id":"x","changeType":"organization_policy","controlKey":"`+testPolicyID+`","action":"ADD"},
		{"id":"other","changeType":"organization_policy_sync","controlKey":"other","action":"EDIT"}]}}}`, nil)
	pending, err := c.OrganizationPolicySyncPending(context.Background(), "aws/r-abcd", testPolicyID)
	if err != nil || pending {
		t.Errorf("pending = %v, err = %v; a staged attachment is not an outstanding edit", pending, err)
	}

	c = clientReturning(t, `{"data":{"PendingChanges":{"items":[
		{"id":"`+testPolicyID+`","changeType":"organization_policy_sync","controlKey":"`+testPolicyID+`","action":"EDIT"}]}}}`, nil)
	pending, err = c.OrganizationPolicySyncPending(context.Background(), "aws/r-abcd", testPolicyID)
	if err != nil || !pending {
		t.Errorf("pending = %v, err = %v, want true", pending, err)
	}
}
