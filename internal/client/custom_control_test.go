package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCustomControlKeyMatchesThePlatform(t *testing.T) {
	cases := map[string]string{
		"Break Glass Actions": "breakglassactions",
		"bedrock-read":        "bedrock-read",
		"  Padded  ":          "padded",
		"S3.Delete_Bucket":    "s3.delete_bucket",
	}
	for title, want := range cases {
		if got := CustomControlKey(title); got != want {
			t.Errorf("CustomControlKey(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestUpsertCustomControlSendsTheWholeDefinition(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"UpsertCustomControl":{"success":true,"wasCreate":true,
		"controlKey":"breakglass","syncRequired":[]}}}`, &got)

	result, err := c.UpsertCustomControl(context.Background(), CustomControl{
		Title: "Break Glass", Description: "d", CloudType: "aws",
		Permissions: []string{"s3:DeleteBucket", "iam:CreateUser"},
	}, UpsertCustomControlOptions{CreateOnly: true, DryRun: true})
	if err != nil {
		t.Fatalf("UpsertCustomControl: %v", err)
	}
	if result.Key != "breakglass" || !result.WasCreate || len(result.SyncRequired) != 0 {
		t.Errorf("result = %+v", result)
	}
	input, _ := got.Variables["input"].(map[string]any)
	if input["title"] != "Break Glass" || input["cloudType"] != "aws" || input["description"] != "d" {
		t.Errorf("input = %v", input)
	}
	// Both have to travel explicitly. An omitted createOnly would overwrite a
	// control someone else defined, and an omitted dryRun would write during
	// a plan.
	if input["createOnly"] != true || input["dryRun"] != true {
		t.Errorf("createOnly = %v, dryRun = %v", input["createOnly"], input["dryRun"])
	}
	permissions, _ := input["permissions"].([]any)
	if len(permissions) != 2 {
		t.Fatalf("permissions = %v", input["permissions"])
	}
	if first, _ := permissions[0].(map[string]any); first["permissionId"] != "s3:DeleteBucket" {
		t.Errorf("permissions[0] = %v", permissions[0])
	}
}

func TestUpsertCustomControlReportsSyncRequired(t *testing.T) {
	c := clientReturning(t, `{"data":{"UpsertCustomControl":{"success":true,"wasCreate":false,
		"controlKey":"breakglass","syncRequired":[{"rootScope":"aws/r-abcd","scopes":["aws/r-abcd/1","aws/r-abcd/2"]}]}}}`, nil)
	result, err := c.UpsertCustomControl(context.Background(), CustomControl{
		Title: "Break Glass", Description: "d", CloudType: "aws", Permissions: []string{"s3:DeleteBucket"},
	}, UpsertCustomControlOptions{})
	if err != nil {
		t.Fatalf("UpsertCustomControl: %v", err)
	}
	want := []CustomControlSync{{RootScope: "aws/r-abcd", Scopes: []string{"aws/r-abcd/1", "aws/r-abcd/2"}}}
	if !reflect.DeepEqual(result.SyncRequired, want) {
		t.Errorf("SyncRequired = %+v, want %+v", result.SyncRequired, want)
	}
}

func TestUpsertCustomControlRefusesBadInputBeforeCalling(t *testing.T) {
	called := false
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) { called = true })
	cases := map[string]CustomControl{
		"azure":          {Title: "x", CloudType: "azure", Permissions: []string{"a"}},
		"no title":       {Title: "  ", CloudType: "aws", Permissions: []string{"a"}},
		"no permissions": {Title: "x", CloudType: "aws"},
	}
	for name, control := range cases {
		if _, err := c.UpsertCustomControl(context.Background(), control, UpsertCustomControlOptions{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if called {
		t.Error("invalid input reached the API")
	}
}

func TestUpsertCustomControlFailureIsAnError(t *testing.T) {
	c := clientReturning(t, `{"data":{"UpsertCustomControl":{"success":false,"controlKey":"x"}}}`, nil)
	if _, err := c.UpsertCustomControl(context.Background(), CustomControl{
		Title: "x", CloudType: "aws", Permissions: []string{"a"},
	}, UpsertCustomControlOptions{}); err == nil {
		t.Error("success:false should be an error")
	}
}

func TestCustomControlReadsAndSortsPermissions(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"CloudControls":{"items":[
		{"controlId":"aws:breakglass:SensitivePermissions","title":"Break Glass","description":"d",
		 "controlKey":"breakglass","cloudType":"aws","serviceFriendlyName":"Custom Control: breakglass",
		 "permissions":[{"permission":"s3:DeleteBucket"},{"permission":"iam:CreateUser"}]}]}}}`, &got)

	control, err := c.CustomControl(context.Background(), "aws", "breakglass")
	if err != nil {
		t.Fatalf("CustomControl: %v", err)
	}
	want := &CustomControl{Key: "breakglass", Title: "Break Glass", Description: "d", CloudType: "aws",
		Permissions: []string{"iam:CreateUser", "s3:DeleteBucket"}}
	if !reflect.DeepEqual(control, want) {
		t.Errorf("got %+v, want %+v", control, want)
	}
	if got.Variables["controlId"] != "aws:breakglass:SensitivePermissions" {
		t.Errorf("controlId = %v", got.Variables["controlId"])
	}
}

// TestCustomControlRefusesBuiltIns: import takes a key from the command line.
// One naming a built-in control must not become a resource whose destroy and
// update are aimed at the platform's own catalog.
func TestCustomControlRefusesBuiltIns(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudControls":{"items":[
		{"controlId":"aws:cloud9:SensitivePermissions","title":"AWS Cloud9 - Sensitive Permissions",
		 "controlKey":"cloud9","cloudType":"aws","serviceFriendlyName":"AWS Cloud9","permissions":[]}]}}}`, nil)
	_, err := c.CustomControl(context.Background(), "aws", "cloud9")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("err = %v, want a built-in refusal that is not ErrNotFound", err)
	}
}

func TestCustomControlMissingIsNotFound(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudControls":{"items":[]}}}`, nil)
	if _, err := c.CustomControl(context.Background(), "aws", "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteCustomControlSendsKeyAndCloud(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"DeleteCustomControl":{"success":true}}}`, &got)
	if err := c.DeleteCustomControl(context.Background(), "aws", "breakglass"); err != nil {
		t.Fatalf("DeleteCustomControl: %v", err)
	}
	input, _ := got.Variables["input"].(map[string]any)
	if input["controlId"] != "breakglass" || input["cloudType"] != "aws" {
		t.Errorf("input = %v", input)
	}
	if err := c.DeleteCustomControl(context.Background(), "aws", ""); err == nil {
		t.Error("an empty key must be refused")
	}
}

func TestCustomControlDeploymentsMergesTheTwoRowsOfAScope(t *testing.T) {
	c := clientReturning(t, `{"data":{"AppliedControlMappings":{"items":[
		{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/1","controlId":"aws:bg:SensitivePermissions","inTransaction":false},
		{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/1","controlId":"aws:bg:ServiceBlock","inTransaction":true},
		{"rootScope":"gcp/o-1","scope":"gcp/o-1/p","controlId":"gcp:bg:ServiceBlock","inTransaction":false}]}}}`, nil)
	got, err := c.CustomControlDeployments(context.Background(), "aws", "bg")
	if err != nil {
		t.Fatalf("CustomControlDeployments: %v", err)
	}
	// One entry per scope, in a transaction if either row is, and nothing
	// from the control of the same name in another cloud.
	if len(got) != 1 || got[0].Scope != "aws/r-abcd/1" || !got[0].InTransaction {
		t.Errorf("got %+v", got)
	}
}

func deploymentsServer(t *testing.T, responses ...string) *Client {
	t.Helper()
	call := 0
	return newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		body := responses[len(responses)-1]
		if call < len(responses) {
			body = responses[call]
		}
		call++
		_, _ = w.Write([]byte(body))
	})
}

const (
	deployedSettled = `{"data":{"AppliedControlMappings":{"items":[
		{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/1","controlId":"aws:bg:ServiceBlock","inTransaction":false}]}}}`
	deployedRolling = `{"data":{"AppliedControlMappings":{"items":[
		{"rootScope":"aws/r-abcd","scope":"aws/r-abcd/1","controlId":"aws:bg:ServiceBlock","inTransaction":true}]}}}`
	deployedNowhere = `{"data":{"AppliedControlMappings":{"items":[]}}}`
)

// TestWaitForCustomControlUndeployedDoesNotWaitOnASettledDeployment: whatever
// would remove it may be waiting on this call, so waiting here can deadlock.
func TestWaitForCustomControlUndeployedDoesNotWaitOnASettledDeployment(t *testing.T) {
	c := deploymentsServer(t, deployedSettled)
	start := time.Now()
	err := c.WaitForCustomControlUndeployed(context.Background(), "aws", "bg", time.Minute)
	var still *StillDeployedError
	if !errors.As(err, &still) || !reflect.DeepEqual(still.Scopes, []string{"aws/r-abcd/1"}) {
		t.Fatalf("err = %v, want StillDeployedError naming the scope", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a settled deployment should fail at once, not after waiting")
	}
}

func TestWaitForCustomControlUndeployedWaitsOutARollout(t *testing.T) {
	c := deploymentsServer(t, deployedRolling, deployedRolling, deployedNowhere)
	if err := c.WaitForCustomControlUndeployed(context.Background(), "aws", "bg", time.Minute); err != nil {
		t.Fatalf("WaitForCustomControlUndeployed: %v", err)
	}
}

func TestWaitForCustomControlUndeployedTimesOut(t *testing.T) {
	c := deploymentsServer(t, deployedRolling)
	err := c.WaitForCustomControlUndeployed(context.Background(), "aws", "bg", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout", err)
	}
}

func TestCustomControlSyncPendingMatchesTypeAndKey(t *testing.T) {
	body := `{"data":{"PendingChanges":{"items":[
		{"id":"1","changeType":"mapping","action":"REMOVE","controlKey":"bg"},
		{"id":"2","changeType":"custom_control_sync","action":"SYNC_REQUIRED","controlKey":"other"}]}}}`
	c := clientReturning(t, body, nil)
	pending, err := c.CustomControlSyncPending(context.Background(), "aws/r-abcd", "bg")
	if err != nil {
		t.Fatalf("CustomControlSyncPending: %v", err)
	}
	if pending {
		t.Error("a sync for another control, or another kind of change for this one, is not a pending sync")
	}
	pending, err = c.CustomControlSyncPending(context.Background(), "aws/r-abcd", "other")
	if err != nil || !pending {
		t.Errorf("pending = %v, err = %v, want true", pending, err)
	}
}

func TestCustomControlsListsOnlyCustomOnes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var sent capture
		_ = json.NewDecoder(r.Body).Decode(&sent)
		if strings.Contains(sent.Query, "CloudServices") {
			_, _ = w.Write([]byte(`{"data":{"CloudServices":{"items":[{"controlKey":"zeta"},{"controlKey":"alpha"}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"CloudControls":{"items":[
			{"controlId":"aws:zeta:SensitivePermissions","title":"Zeta","controlKey":"zeta","cloudType":"aws",
			 "serviceFriendlyName":"Custom Control: zeta","permissions":[{"permission":"s3:B"},{"permission":"s3:A"}]},
			{"controlId":"aws:alpha:SensitivePermissions","title":"Alpha","controlKey":"alpha","cloudType":"aws",
			 "serviceFriendlyName":"Custom Control: alpha","permissions":[]},
			{"controlId":"aws:s3:SensitivePermissions","title":"S3","controlKey":"s3","cloudType":"aws",
			 "serviceFriendlyName":"Amazon S3","permissions":[]}]}}}`))
	})
	got, err := c.CustomControls(context.Background(), "aws/r-abcd")
	if err != nil {
		t.Fatalf("CustomControls: %v", err)
	}
	if len(got) != 2 || got[0].Key != "alpha" || got[1].Key != "zeta" {
		t.Fatalf("got %+v, want alpha then zeta and no built-in", got)
	}
	if !reflect.DeepEqual(got[1].Permissions, []string{"s3:A", "s3:B"}) {
		t.Errorf("permissions = %v", got[1].Permissions)
	}
	if _, err := c.CustomControls(context.Background(), "azure/tenant"); err == nil {
		t.Error("azure has no custom controls")
	}
}
