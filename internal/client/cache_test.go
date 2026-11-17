package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"testing"
)

// routedServer answers each GraphQL operation by name and counts how often
// each one was called.
type routedServer struct {
	mu     sync.Mutex
	calls  map[string]int
	routes map[string]func(vars map[string]any) string
}

var operationName = regexp.MustCompile(`^\s*(?:query|mutation)\s+(\w+)`)

func newRoutedClient(t *testing.T, cfg Config, routes map[string]func(vars map[string]any) string) (*Client, *routedServer) {
	t.Helper()
	rs := &routedServer{calls: map[string]int{}, routes: routes}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req capture
		_ = json.NewDecoder(r.Body).Decode(&req)
		m := operationName.FindStringSubmatch(req.Query)
		if m == nil {
			t.Errorf("unrecognized document: %s", req.Query)
			return
		}
		rs.mu.Lock()
		rs.calls[m[1]]++
		route, ok := rs.routes[m[1]]
		rs.mu.Unlock()
		if !ok {
			t.Errorf("no route for %s", m[1])
			return
		}
		_, _ = w.Write([]byte(route(req.Variables)))
	})
	c.cache = newReadCache(!cfg.DisableReadCache)
	return c, rs
}

func (rs *routedServer) count(op string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.calls[op]
}

func fixed(body string) func(map[string]any) string {
	return func(map[string]any) string { return body }
}

// accountRoutes serves two accounts with three disabled services each, the
// shape of a plan refreshing many controls across few accounts.
func accountRoutes() map[string]func(map[string]any) string {
	return map[string]func(map[string]any) string{
		"cloudHierarchy": func(vars map[string]any) string {
			id := vars["resourceIds"].([]any)[0].(string)
			return fmt.Sprintf(`{"data":{"CloudHierarchyList":{"items":[
				{"scope":"aws/r-abcd/%s","resourceId":%q,"entryType":"account"}]}}}`, id, id)
		},
		"scopeServiceStatuses": fixed(`{"data":{"CloudServices":{"items":[
			{"controlKey":"s3","status":{"status":"Disabled","isPending":false}},
			{"controlKey":"ec2","status":{"status":"Disabled","isPending":true}},
			{"controlKey":"iam","status":{"status":"Protected","isPending":false}}]}}}`),
		"serviceControlStatus": fixed(`{"data":{"CloudServiceUsage":{"items":[
			{"project":"lambda","status":{"status":"Disabled","isPending":false}}]}}}`),
		"disableService": fixed(`{"data":{"DisableService":{"success":true}}}`),
	}
}

func TestPlanShapedReadsQueryOncePerAccount(t *testing.T) {
	c, rs := newRoutedClient(t, Config{}, accountRoutes())
	ctx := context.Background()

	var wg sync.WaitGroup
	for _, account := range []string{"111111111111", "222222222222"} {
		for _, key := range []string{"s3", "ec2", "iam"} {
			for range 10 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					entry, err := c.ResolveScope(ctx, "aws/r-abcd", account, "")
					if err != nil {
						t.Errorf("ResolveScope: %v", err)
						return
					}
					if _, err := c.ServiceControlStatus(ctx, key, entry.Scope); err != nil {
						t.Errorf("ServiceControlStatus: %v", err)
					}
				}()
			}
		}
	}
	wg.Wait()

	if got := rs.count("cloudHierarchy"); got != 2 {
		t.Errorf("cloudHierarchy calls = %d, want 2 (one per account)", got)
	}
	if got := rs.count("scopeServiceStatuses"); got != 2 {
		t.Errorf("scopeServiceStatuses calls = %d, want 2 (one per account)", got)
	}
	if got := rs.count("serviceControlStatus"); got != 0 {
		t.Errorf("per-service status calls = %d, want 0", got)
	}
}

func TestBatchedStatusMatchesPerServiceAnswer(t *testing.T) {
	c, _ := newRoutedClient(t, Config{}, accountRoutes())

	got, err := c.ServiceControlStatus(context.Background(), "ec2", "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("ServiceControlStatus: %v", err)
	}
	if got.Status != StatusDisabled || !got.Pending {
		t.Errorf("status = %#v, want Disabled and pending", got)
	}
}

func TestServiceMissingFromBatchIsReadOnItsOwn(t *testing.T) {
	// A short page must cost a query, not report the service as gone.
	c, rs := newRoutedClient(t, Config{}, accountRoutes())

	got, err := c.ServiceControlStatus(context.Background(), "lambda", "aws/r-abcd/1")
	if err != nil {
		t.Fatalf("ServiceControlStatus: %v", err)
	}
	if got.Status != StatusDisabled {
		t.Errorf("status = %q, want Disabled", got.Status)
	}
	if rs.count("serviceControlStatus") != 1 {
		t.Errorf("per-service status calls = %d, want 1", rs.count("serviceControlStatus"))
	}
}

func TestContainerServiceListIsNotUsed(t *testing.T) {
	routes := accountRoutes()
	routes["scopeServiceStatuses"] = fixed(`{"data":{"CloudServices":{"items":[
		{"controlKey":"lambda","status":{"status":"Partially Protected","isPending":false}}]}}}`)
	c, rs := newRoutedClient(t, Config{}, routes)
	ctx := context.Background()

	for range 3 {
		if _, err := c.ServiceControlStatus(ctx, "lambda", "aws/r-abcd"); err != nil {
			t.Fatalf("ServiceControlStatus: %v", err)
		}
	}
	if rs.count("serviceControlStatus") != 3 {
		t.Errorf("per-service status calls = %d, want 3", rs.count("serviceControlStatus"))
	}
	// The unusable batch is remembered, not retried for every service.
	if rs.count("scopeServiceStatuses") != 1 {
		t.Errorf("scopeServiceStatuses calls = %d, want 1", rs.count("scopeServiceStatuses"))
	}
}

func TestFailedServiceListFallsBackOnce(t *testing.T) {
	routes := accountRoutes()
	routes["scopeServiceStatuses"] = fixed(`{"errors":[{"message":"Cannot query field \"controlKey\""}]}`)
	c, rs := newRoutedClient(t, Config{}, routes)
	ctx := context.Background()

	for range 3 {
		if _, err := c.ServiceControlStatus(ctx, "lambda", "aws/r-abcd/1"); err != nil {
			t.Fatalf("ServiceControlStatus: %v", err)
		}
	}
	if rs.count("scopeServiceStatuses") != 1 || rs.count("serviceControlStatus") != 3 {
		t.Errorf("calls = %v, want one batch attempt then per-service reads", rs.calls)
	}
}

func TestWriteSwitchesCacheOff(t *testing.T) {
	c, rs := newRoutedClient(t, Config{}, accountRoutes())
	ctx := context.Background()

	if _, err := c.ServiceControlStatus(ctx, "s3", "aws/r-abcd/1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DisableService(ctx, "s3", "aws/r-abcd/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ServiceControlStatus(ctx, "s3", "aws/r-abcd/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ServiceControlStatus(ctx, "s3", "aws/r-abcd/1"); err != nil {
		t.Fatal(err)
	}
	// One batch before the write; after it, every read goes to the platform
	// one service at a time, exactly as with no cache.
	if rs.count("scopeServiceStatuses") != 1 || rs.count("serviceControlStatus") != 2 {
		t.Errorf("calls = %v, want one batch then two per-service reads", rs.calls)
	}
}

func TestDisableReadCacheSendsEveryRead(t *testing.T) {
	c, rs := newRoutedClient(t, Config{DisableReadCache: true}, accountRoutes())
	ctx := context.Background()

	for range 3 {
		if _, err := c.ResolveScope(ctx, "aws/r-abcd", "111111111111", ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := rs.count("cloudHierarchy"); got != 3 {
		t.Errorf("cloudHierarchy calls = %d, want 3", got)
	}

	// Without sharing, a whole account's list would be fetched to answer for
	// one service, so the per-service query is used as it always was.
	if _, err := c.ServiceControlStatus(ctx, "lambda", "aws/r-abcd/1"); err != nil {
		t.Fatal(err)
	}
	if rs.count("scopeServiceStatuses") != 0 || rs.count("serviceControlStatus") != 1 {
		t.Errorf("calls = %v, want only the per-service query", rs.calls)
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	var mu sync.Mutex
	fail := true
	routes := map[string]func(map[string]any) string{
		"regionsControls": func(map[string]any) string {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				fail = false
				return `{"errors":[{"message":"boom"}]}`
			}
			return `{"data":{"RegionsControls":{"items":[
				{"regionId":"us-east-1","status":{"status":"ENABLED","pending":false}}]}}}`
		},
	}
	c, rs := newRoutedClient(t, Config{}, routes)
	ctx := context.Background()

	if _, err := c.RegionControls(ctx, "aws/r-abcd/1"); err == nil {
		t.Fatal("first read: expected an error")
	}
	regions, err := c.RegionControls(ctx, "aws/r-abcd/1")
	if err != nil || len(regions) != 1 {
		t.Fatalf("second read = %v, %v; want one region", regions, err)
	}
	if _, err := c.RegionControls(ctx, "aws/r-abcd/1"); err != nil {
		t.Fatal(err)
	}
	if got := rs.count("regionsControls"); got != 2 {
		t.Errorf("regionsControls calls = %d, want 2", got)
	}
}

func TestResolveScopeFilterDoesNotCorruptCache(t *testing.T) {
	routes := map[string]func(map[string]any) string{
		"cloudHierarchy": fixed(`{"data":{"CloudHierarchyList":{"items":[
			{"scope":"aws/r-abcd/ou-x","resourceId":"x","entryType":"organizationalUnit"},
			{"scope":"aws/r-abcd/x","resourceId":"x","entryType":"account"}]}}}`),
	}
	c, _ := newRoutedClient(t, Config{}, routes)
	ctx := context.Background()

	entry, err := c.ResolveScope(ctx, "aws/r-abcd", "x", EntryTypeAccount)
	if err != nil || entry.Scope != "aws/r-abcd/x" {
		t.Fatalf("filtered = %v, %v", entry, err)
	}
	// Unfiltered, the id is ambiguous, which the cached copy must still show.
	if _, err := c.ResolveScope(ctx, "aws/r-abcd", "x", ""); err == nil {
		t.Fatal("unfiltered: expected an ambiguity error")
	}
}

func TestRegionListCopiesAreIndependent(t *testing.T) {
	routes := map[string]func(map[string]any) string{
		"regionsControls": fixed(`{"data":{"RegionsControls":{"items":[
			{"regionId":"us-east-1","status":{"status":"ENABLED","pending":false}}]}}}`),
	}
	c, _ := newRoutedClient(t, Config{}, routes)
	ctx := context.Background()

	first, _ := c.RegionControls(ctx, "s")
	first[0].RegionID = "mutated"
	second, _ := c.RegionControls(ctx, "s")
	if second[0].RegionID != "us-east-1" {
		t.Errorf("cached list was modified through a caller's copy: %v", second)
	}
}

func exceptionRoutes(applied, staged string) map[string]func(map[string]any) string {
	return map[string]func(map[string]any) string{
		"scopeAppliedControlExceptions": fixed(applied),
		"scopeStagedControlExceptions":  fixed(staged),
		"appliedControlExceptions": fixed(`{"data":{"AppliedControlExceptions":{"items":[
			{"identities":["arn:per-key"],"ssoActorIds":[]}]}}}`),
		"stagedControlExceptions": fixed(`{"data":{"StagedControlExceptions":{"items":[]}}}`),
	}
}

func TestScopeExceptionsServeEveryControl(t *testing.T) {
	c, rs := newRoutedClient(t, Config{}, exceptionRoutes(
		`{"data":{"AppliedControlExceptions":{"count":2,"items":[
			{"controlKey":"iam","identities":["arn:a","arn:b"],"ssoActorIds":["x@example.com"],"controlId":"c1"},
			{"controlKey":"s3","identities":["arn:s3"],"ssoActorIds":[],"controlId":"c2"}]}}}`,
		`{"data":{"StagedControlExceptions":{"count":1,"items":[
			{"controlKey":"iam","identities":["arn:a"],"ssoActorIds":[],"action":"REMOVE"}]}}}`))
	ctx := context.Background()

	iam, err := c.ControlExceptions(ctx, "iam", "aws/r-abcd/1")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(iam.Identities) != "[arn:b]" || fmt.Sprint(iam.SSOActorIDs) != "[x@example.com]" || iam.ControlID != "c1" {
		t.Errorf("iam = %#v", iam)
	}
	none, err := c.ControlExceptions(ctx, "ec2", "aws/r-abcd/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(none.Identities) != 0 || len(none.SSOActorIDs) != 0 {
		t.Errorf("ec2 = %#v, want no exceptions", none)
	}
	if rs.count("scopeAppliedControlExceptions") != 1 || rs.count("appliedControlExceptions") != 0 {
		t.Errorf("calls = %v, want one batch and no per-control reads", rs.calls)
	}
}

func TestIncompleteExceptionPageFallsBack(t *testing.T) {
	// count says there is more than the page held: a missing control key
	// could be on the next page, so nothing is concluded from it.
	c, rs := newRoutedClient(t, Config{}, exceptionRoutes(
		`{"data":{"AppliedControlExceptions":{"count":21,"items":[
			{"controlKey":"iam","identities":["arn:a"],"ssoActorIds":[]}]}}}`,
		`{"data":{"StagedControlExceptions":{"count":0,"items":[]}}}`))
	assertExceptionFallback(t, c, rs)
}

func TestExceptionListWithoutCountFallsBack(t *testing.T) {
	c, rs := newRoutedClient(t, Config{}, exceptionRoutes(
		`{"data":{"AppliedControlExceptions":{"items":[]}}}`,
		`{"data":{"StagedControlExceptions":{"items":[]}}}`))
	assertExceptionFallback(t, c, rs)
}

func TestRejectedExceptionBatchFallsBack(t *testing.T) {
	c, rs := newRoutedClient(t, Config{}, exceptionRoutes(
		`{"errors":[{"message":"Cannot query field \"count\""}]}`,
		`{"data":{"StagedControlExceptions":{"count":0,"items":[]}}}`))
	assertExceptionFallback(t, c, rs)
}

func assertExceptionFallback(t *testing.T, c *Client, rs *routedServer) {
	t.Helper()
	ctx := context.Background()
	for range 2 {
		got, err := c.ControlExceptions(ctx, "ec2", "aws/r-abcd/1")
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got.Identities) != "[arn:per-key]" {
			t.Errorf("identities = %v, want the per-control answer", got.Identities)
		}
	}
	if rs.count("scopeAppliedControlExceptions") != 1 || rs.count("appliedControlExceptions") != 2 {
		t.Errorf("calls = %v, want one batch attempt then per-control reads", rs.calls)
	}
}

func TestUnauthorizedBatchIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.ServiceControlStatus(context.Background(), "s3", "aws/r-abcd/1")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
}

func TestIsReadOnly(t *testing.T) {
	cases := map[string]bool{
		scopeServiceStatusesQuery:                    true,
		hierarchyQuery:                               true,
		disableServiceMutation:                       false,
		"{ CloudServices { items { controlKey } } }": false,
	}
	for doc, want := range cases {
		if got := isReadOnly(doc); got != want {
			t.Errorf("isReadOnly(%.40q) = %v, want %v", doc, got, want)
		}
	}
}
