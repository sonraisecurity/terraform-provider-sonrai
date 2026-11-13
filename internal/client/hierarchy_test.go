package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDefaultLeafEntryType(t *testing.T) {
	// The leaf of the hierarchy is named differently per cloud; getting this
	// wrong returns no rows rather than an error from the API.
	cases := map[string]string{
		"aws/r-abcd":                   EntryTypeAccount,
		"gcp/organizations|1234567890": EntryTypeProject,
		"azure/|tenants|00000000-0000-0000-0000-000000000000": EntryTypeSubscription,
	}
	for scope, want := range cases {
		if got := DefaultLeafEntryType(scope); got != want {
			t.Errorf("DefaultLeafEntryType(%q) = %q, want %q", scope, got, want)
		}
	}
}

func TestResolveScopeReturnsCanonicalPath(t *testing.T) {
	var got capture
	c := clientReturning(t, `{"data":{"CloudHierarchyList":{"items":[{
		"scope":"aws/r-abcd/ou-abcd-11111111/123456789012","name":"prod",
		"resourceId":"123456789012","entryType":"account","cloudType":"aws",
		"parentScope":"aws/r-abcd/ou-abcd-11111111"}]}}}`, &got)

	entry, err := c.ResolveScope(context.Background(), "aws/r-abcd", "123456789012", EntryTypeAccount)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	// The whole point: the path is not root + "/" + account id.
	if entry.Scope != "aws/r-abcd/ou-abcd-11111111/123456789012" {
		t.Errorf("scope = %q", entry.Scope)
	}
	ids, _ := got.Variables["resourceIds"].([]any)
	if len(ids) != 1 || ids[0] != "123456789012" {
		t.Errorf("resourceIds = %#v, want the bare AWS id", got.Variables["resourceIds"])
	}
}

func TestResolveScopeNotFound(t *testing.T) {
	c := clientReturning(t, `{"data":{"CloudHierarchyList":{"items":[]}}}`, nil)

	_, err := c.ResolveScope(context.Background(), "aws/r-abcd", "000000000000", EntryTypeAccount)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestResolveScopeAmbiguous(t *testing.T) {
	// Same id under two roots would otherwise silently resolve to whichever
	// came back first, and stage a control in the wrong place.
	c := clientReturning(t, `{"data":{"CloudHierarchyList":{"items":[
		{"scope":"aws/r-abcd/111111111111","resourceId":"111111111111"},
		{"scope":"aws/r-zzzz/111111111111","resourceId":"111111111111"}]}}}`, nil)

	_, err := c.ResolveScope(context.Background(), "aws/r", "111111111111", "")
	if err == nil || !strings.Contains(err.Error(), "matches 2 entries") {
		t.Fatalf("error = %v, want an ambiguity error", err)
	}
}

func TestCandidateResourceIDs(t *testing.T) {
	cases := []struct {
		name, rootScope, id string
		want                []string
	}{
		// AWS keeps bare ids for both accounts and OUs.
		{"aws account", "aws/r-abcd", "123456789012", []string{"123456789012"}},
		{"aws ou", "aws/r-abcd", "ou-abcd-1a2b3c", []string{"ou-abcd-1a2b3c"}},
		// A bare GCP id could name a project or a folder, so try both.
		{"gcp bare", "gcp/organizations|1", "my-project",
			[]string{"my-project", "projects|my-project", "folders|my-project"}},
		{"azure bare", "azure/|tenants|x", "a1b2c3d4",
			[]string{"a1b2c3d4", "|subscriptions|a1b2c3d4"}},
		// Already-qualified ids must not be prefixed twice.
		{"gcp qualified", "gcp/organizations|1", "projects|my-project", []string{"projects|my-project"}},
		{"azure qualified", "azure/|tenants|x", "|subscriptions|abc", []string{"|subscriptions|abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CandidateResourceIDs(tc.rootScope, tc.id)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %q, want %q", got, tc.want)
					break
				}
			}
		})
	}
}

func TestResolveScopeMatchesAnyEntryTypeByDefault(t *testing.T) {
	// An OU and an account resolve through the same path; the caller should not
	// have to say which kind of thing an id names.
	c := clientReturning(t, `{"data":{"CloudHierarchyList":{"items":[{
		"scope":"aws/r-abcd/ou-abcd-11111111/ou-abcd-22222222","name":"nested",
		"resourceId":"ou-abcd-22222222","entryType":"organizationalUnit","cloudType":"aws",
		"parentScope":"aws/r-abcd/ou-abcd-11111111"}]}}}`, nil)

	entry, err := c.ResolveScope(context.Background(), "aws/r-abcd", "ou-abcd-22222222", "")
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if entry.EntryType != "organizationalUnit" || entry.Scope != "aws/r-abcd/ou-abcd-11111111/ou-abcd-22222222" {
		t.Errorf("entry = %+v", entry)
	}
}

func TestResolveScopeFiltersByEntryTypeWhenGiven(t *testing.T) {
	// Same id, two kinds of thing: entryType disambiguates.
	c := clientReturning(t, `{"data":{"CloudHierarchyList":{"items":[
		{"scope":"aws/r-abcd/111111111111","resourceId":"111111111111","entryType":"account"},
		{"scope":"aws/r-abcd/111111111111","resourceId":"111111111111","entryType":"managementAccount"}]}}}`, nil)

	entry, err := c.ResolveScope(context.Background(), "aws/r-abcd", "111111111111", "managementAccount")
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if entry.EntryType != "managementAccount" {
		t.Errorf("entryType = %q, want managementAccount", entry.EntryType)
	}
}

func TestIsLeafAccountType(t *testing.T) {
	// Containers are rejected by sonrai_account_disabled_service: staging a control at one
	// expands to the leaf accounts present at the time rather than marking the
	// container itself.
	leaves := []string{EntryTypeAccount, EntryTypeManagementAccount, EntryTypeProject, EntryTypeSubscription}
	for _, et := range leaves {
		if !IsLeafAccountType(et) {
			t.Errorf("%q should be a leaf account type", et)
		}
	}
	containers := []string{
		"organization", "organizationalUnit", "folder", "managementGroup",
		"root", "tenant", "resourceGroup", "",
	}
	for _, et := range containers {
		if IsLeafAccountType(et) {
			t.Errorf("%q should not be a leaf account type", et)
		}
	}
}

func TestResolveScopeReportsEnrolment(t *testing.T) {
	// An account can exist in the hierarchy without being enrolled in the
	// firewall; the provider needs that fact to explain "invalid scope".
	c := clientReturning(t, `{"data":{"CloudHierarchyList":{"items":[{
		"scope":"aws/r-abcd/ou-abcd-33333333/210987654321","resourceId":"210987654321",
		"entryType":"account","cloudType":"aws","purpleEnabled":false,"active":true}]}}}`, nil)

	entry, err := c.ResolveScope(context.Background(), "aws/r-abcd", "210987654321", "")
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if entry.PurpleEnabled {
		t.Error("purpleEnabled should be false for an unenrolled account")
	}
	if !entry.Active {
		t.Error("active should be true")
	}
}
