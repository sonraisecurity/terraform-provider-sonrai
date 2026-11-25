package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Cloud types a custom control can be defined for.
var CustomControlCloudTypes = []string{"aws", "gcp"}

// The platform files a custom control as a service whose friendly name
// carries this prefix. Nothing else on a control says whether a customer
// defined it.
const customControlFriendlyPrefix = "Custom Control: "

// CustomControl is a customer-defined group of permissions in the
// organization's control catalog.
type CustomControl struct {
	Key         string
	Title       string
	Description string
	CloudType   string
	// Permissions are in the platform's own spelling, sorted.
	Permissions []string
}

// CustomControlSync names scopes under one root scope where a custom control
// is deployed with a permission list that an edit has just replaced. They keep
// enforcing the old list until that root scope is redeployed.
type CustomControlSync struct {
	RootScope string   `json:"rootScope"`
	Scopes    []string `json:"scopes"`
}

// CustomControlResult is the outcome of an upsert, or of a dry run of one.
type CustomControlResult struct {
	Key string
	// WasCreate is false when a control with this key already existed.
	WasCreate    bool
	SyncRequired []CustomControlSync
}

// CustomControlKey derives the key the platform gives a control with this
// title: lower case, with spaces removed. The platform's own answer, returned
// by an upsert, is authoritative; this exists so a plan can tell whether a new
// title names the same control.
func CustomControlKey(title string) string {
	return strings.ToLower(strings.ReplaceAll(title, " ", ""))
}

const upsertCustomControlMutation = `
mutation upsertCustomControl($input: UpsertCustomControlInput!) {
  UpsertCustomControl(input: $input) {
    success
    wasCreate
    controlKey
    syncRequired { rootScope scopes }
  }
}`

// UpsertCustomControlOptions selects how an upsert treats an existing control
// and whether it writes at all.
type UpsertCustomControlOptions struct {
	// CreateOnly refuses to touch a control that already has this key.
	CreateOnly bool
	// DryRun validates and reports the outcome without writing.
	DryRun bool
}

// UpsertCustomControl creates the control, or replaces the description and
// the whole permission list of the one that already has its key.
//
// The write goes straight to the catalog; it is not staged. A new control
// protects nothing until it is attached to a scope. An edit to one that is
// already deployed leaves every deployment on the old permission list until
// its root scope is redeployed, and SyncRequired says which.
func (c *Client) UpsertCustomControl(ctx context.Context, control CustomControl, opts UpsertCustomControlOptions) (*CustomControlResult, error) {
	if err := validateCustomControlCloud(control.CloudType); err != nil {
		return nil, err
	}
	if strings.TrimSpace(control.Title) == "" {
		return nil, errors.New("a custom control needs a title")
	}
	if len(control.Permissions) == 0 {
		return nil, errors.New("a custom control needs at least one permission")
	}
	permissions := make([]map[string]any, 0, len(control.Permissions))
	for _, permission := range control.Permissions {
		permissions = append(permissions, map[string]any{"permissionId": permission})
	}
	input := map[string]any{
		"title":       control.Title,
		"description": control.Description,
		"cloudType":   control.CloudType,
		"permissions": permissions,
		"createOnly":  opts.CreateOnly,
		"dryRun":      opts.DryRun,
	}

	var out struct {
		UpsertCustomControl struct {
			Success      bool                `json:"success"`
			WasCreate    bool                `json:"wasCreate"`
			ControlKey   string              `json:"controlKey"`
			SyncRequired []CustomControlSync `json:"syncRequired"`
		} `json:"UpsertCustomControl"`
	}
	// Not retried on an ambiguous failure: with CreateOnly, a retry after a
	// lost success would be refused as a duplicate of the control it just made.
	if err := c.ExecuteOnce(ctx, upsertCustomControlMutation, map[string]any{"input": input}, &out); err != nil {
		return nil, err
	}
	result := out.UpsertCustomControl
	if !result.Success {
		return nil, fmt.Errorf("UpsertCustomControl reported failure for %q", control.Title)
	}
	if result.ControlKey == "" {
		return nil, fmt.Errorf("UpsertCustomControl returned no control key for %q", control.Title)
	}
	return &CustomControlResult{
		Key:          result.ControlKey,
		WasCreate:    result.WasCreate,
		SyncRequired: result.SyncRequired,
	}, nil
}

const customControlQuery = `
query customControl($controlId: String!) {
  CloudControls(where: {controlId: {op: EQ, value: $controlId}}) {
    items(limit: 2) {
      controlId
      title
      description
      controlKey
      cloudType
      serviceFriendlyName
      permissions { permission }
    }
  }
}`

type cloudControlRow struct {
	ControlID           string `json:"controlId"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	ControlKey          string `json:"controlKey"`
	CloudType           string `json:"cloudType"`
	ServiceFriendlyName string `json:"serviceFriendlyName"`
	Permissions         []struct {
		Permission string `json:"permission"`
	} `json:"permissions"`
}

func (r cloudControlRow) custom() bool {
	return strings.HasPrefix(r.ServiceFriendlyName, customControlFriendlyPrefix)
}

func (r cloudControlRow) control() CustomControl {
	permissions := make([]string, 0, len(r.Permissions))
	for _, p := range r.Permissions {
		permissions = append(permissions, p.Permission)
	}
	sort.Strings(permissions)
	return CustomControl{
		Key:         r.ControlKey,
		Title:       r.Title,
		Description: r.Description,
		CloudType:   r.CloudType,
		Permissions: permissions,
	}
}

// A custom control is stored as two catalog rows, one for protecting its
// permissions and one for blocking them. They carry the same title,
// description and permissions, so the first is read as the control.
func customControlID(cloudType, key string) string {
	return cloudType + ":" + key + ":SensitivePermissions"
}

// CustomControl reads one custom control. It returns ErrNotFound when no
// control has the key, and an error when the key belongs to one of the
// platform's own controls, which this API must never be used to edit.
func (c *Client) CustomControl(ctx context.Context, cloudType, key string) (*CustomControl, error) {
	if err := validateCustomControlCloud(cloudType); err != nil {
		return nil, err
	}
	var out struct {
		CloudControls struct {
			Items []cloudControlRow `json:"items"`
		} `json:"CloudControls"`
	}
	id := customControlID(cloudType, key)
	if err := c.Execute(ctx, customControlQuery, map[string]any{"controlId": id}, &out); err != nil {
		return nil, err
	}
	for _, row := range out.CloudControls.Items {
		if row.ControlID != id {
			continue
		}
		if !row.custom() {
			return nil, fmt.Errorf("%q is a built-in %s control, not a custom one", key, cloudType)
		}
		control := row.control()
		return &control, nil
	}
	return nil, fmt.Errorf("%w: custom control %q for %s", ErrNotFound, key, cloudType)
}

const deleteCustomControlMutation = `
mutation deleteCustomControl($input: DeleteCustomControlRequestInput!) {
  DeleteCustomControl(input: $input) {
    success
  }
}`

// DeleteCustomControl removes the control from the catalog. The platform
// refuses while it is deployed at any scope. Attachments that were staged and
// never deployed do not stop it, and are discarded with the control.
func (c *Client) DeleteCustomControl(ctx context.Context, cloudType, key string) error {
	if err := validateCustomControlCloud(cloudType); err != nil {
		return err
	}
	if key == "" {
		return errors.New("a custom control key is required")
	}
	var out struct {
		DeleteCustomControl struct {
			Success bool `json:"success"`
		} `json:"DeleteCustomControl"`
	}
	input := map[string]any{"controlId": key, "cloudType": cloudType}
	if err := c.ExecuteOnce(ctx, deleteCustomControlMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if !out.DeleteCustomControl.Success {
		return fmt.Errorf("DeleteCustomControl reported failure for %q", key)
	}
	return nil
}

// ControlDeployment is one scope a control is deployed at.
type ControlDeployment struct {
	RootScope string `json:"rootScope"`
	Scope     string `json:"scope"`
	ControlID string `json:"controlId"`
	// InTransaction is true while a submission that touches this deployment
	// is being rolled out.
	InTransaction bool `json:"inTransaction"`
}

const (
	controlDeploymentsPageSize = 500
	controlDeploymentsMaxPages = 40
)

const controlDeploymentsQuery = `
query controlDeployments($controlKey: String!, $limit: Long, $offset: Long) {
  AppliedControlMappings(where: {controlKey: {op: EQ, value: $controlKey}}) {
    items(orderBy: {scope: {order: ASC}}, limit: $limit, offset: $offset) {
      rootScope
      scope
      controlId
      inTransaction
    }
  }
}`

// CustomControlDeployments lists the scopes the control is deployed at, one
// entry per scope. A control staged at a scope and not yet deployed is not
// included.
func (c *Client) CustomControlDeployments(ctx context.Context, cloudType, key string) ([]ControlDeployment, error) {
	if err := validateCustomControlCloud(cloudType); err != nil {
		return nil, err
	}
	seen := map[string]int{}
	var all []ControlDeployment
	for page := 0; page < controlDeploymentsMaxPages; page++ {
		var out struct {
			AppliedControlMappings struct {
				Items []ControlDeployment `json:"items"`
			} `json:"AppliedControlMappings"`
		}
		vars := map[string]any{
			"controlKey": key,
			"limit":      controlDeploymentsPageSize,
			"offset":     page * controlDeploymentsPageSize,
		}
		if err := c.Execute(ctx, controlDeploymentsQuery, vars, &out); err != nil {
			return nil, err
		}
		for _, d := range out.AppliedControlMappings.Items {
			// The same key can exist in another cloud's catalog.
			if !strings.HasPrefix(d.ControlID, cloudType+":") {
				continue
			}
			if at, ok := seen[d.Scope]; ok {
				all[at].InTransaction = all[at].InTransaction || d.InTransaction
				continue
			}
			seen[d.Scope] = len(all)
			all = append(all, d)
		}
		if len(out.AppliedControlMappings.Items) < controlDeploymentsPageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("control %q is deployed at more than %d scopes; refusing to work from a partial list",
		key, controlDeploymentsPageSize*controlDeploymentsMaxPages)
}

// StillDeployedError reports scopes a control is deployed at with nothing
// under way that would remove it.
type StillDeployedError struct {
	Key    string
	Scopes []string
}

func (e *StillDeployedError) Error() string {
	return fmt.Sprintf("custom control %q is still deployed at %s", e.Key, strings.Join(e.Scopes, ", "))
}

// WaitForCustomControlUndeployed returns once the control is deployed
// nowhere, so that it can be deleted.
//
// It only waits out a removal that is already being rolled out. A deployment
// that is in no transaction gets a *StillDeployedError straight away, even if
// its removal is staged: whatever would submit that removal may itself be
// waiting on this call, and then neither would ever finish. It never detaches
// anything itself.
func (c *Client) WaitForCustomControlUndeployed(ctx context.Context, cloudType, key string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		deployments, err := c.CustomControlDeployments(ctx, cloudType, key)
		if err != nil {
			return err
		}
		if len(deployments) == 0 {
			return nil
		}
		var settled, rolling []string
		for _, d := range deployments {
			if d.InTransaction {
				rolling = append(rolling, d.Scope)
				continue
			}
			settled = append(settled, d.Scope)
		}
		if len(settled) > 0 {
			return &StillDeployedError{Key: key, Scopes: settled}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out after %s waiting for custom control %q to be removed from %s",
				timeout, key, strings.Join(rolling, ", "))
		case <-ticker.C:
		}
	}
}

// CustomControlSyncPending reports whether an edit to the control is still
// waiting to be redeployed under rootScope.
func (c *Client) CustomControlSyncPending(ctx context.Context, rootScope, key string) (bool, error) {
	pending, err := c.PendingChanges(ctx, rootScope)
	if err != nil {
		return false, err
	}
	for _, change := range pending {
		if change.ChangeType == "custom_control_sync" && change.ControlKey == key {
			return true, nil
		}
	}
	return false, nil
}

const customControlKeysQuery = `
query customControlKeys($rootScope: String!, $limit: Long, $offset: Long) {
  CloudServices(where: {rootScope: {op: EQ, value: $rootScope}, customControl: {op: EQ, value: true}}) {
    items(orderBy: {controlKey: {order: ASC}}, limit: $limit, offset: $offset) {
      controlKey
    }
  }
}`

const customControlsQuery = `
query customControls($keys: [String], $cloudType: String!, $limit: Long, $offset: Long) {
  CloudControls(where: {
    controlKey: {op: IN_LIST, values: $keys},
    controlType: {op: EQ, value: "Permissions"},
    cloudType: {op: EQ, value: $cloudType}
  }) {
    items(orderBy: {controlKey: {order: ASC}}, limit: $limit, offset: $offset) {
      controlId
      title
      description
      controlKey
      cloudType
      serviceFriendlyName
      permissions { permission }
    }
  }
}`

const (
	customControlsPageSize = 100
	customControlsMaxPages = 50
)

// CustomControls lists every custom control in the catalog for the cloud that
// rootScope belongs to, sorted by key.
//
// The catalog is shared by the whole organization. The root scope is needed
// only because the platform will not list services without one, and to pick
// the cloud.
func (c *Client) CustomControls(ctx context.Context, rootScope string) ([]CustomControl, error) {
	cloudType, _, _ := strings.Cut(rootScope, "/")
	if err := validateCustomControlCloud(cloudType); err != nil {
		return nil, err
	}

	var controlKeys []string
	for page := 0; ; page++ {
		if page == customControlsMaxPages {
			return nil, fmt.Errorf("more than %d custom controls under %q; refusing to return a partial list",
				customControlsPageSize*customControlsMaxPages, rootScope)
		}
		var out struct {
			CloudServices struct {
				Items []struct {
					ControlKey string `json:"controlKey"`
				} `json:"items"`
			} `json:"CloudServices"`
		}
		vars := map[string]any{
			"rootScope": rootScope,
			"limit":     customControlsPageSize,
			"offset":    page * customControlsPageSize,
		}
		if err := c.Execute(ctx, customControlKeysQuery, vars, &out); err != nil {
			return nil, err
		}
		for _, item := range out.CloudServices.Items {
			controlKeys = append(controlKeys, item.ControlKey)
		}
		if len(out.CloudServices.Items) < customControlsPageSize {
			break
		}
	}
	if len(controlKeys) == 0 {
		return []CustomControl{}, nil
	}

	controls := make([]CustomControl, 0, len(controlKeys))
	for start := 0; start < len(controlKeys); start += customControlsPageSize {
		end := min(start+customControlsPageSize, len(controlKeys))
		var out struct {
			CloudControls struct {
				Items []cloudControlRow `json:"items"`
			} `json:"CloudControls"`
		}
		vars := map[string]any{
			"keys":      controlKeys[start:end],
			"cloudType": cloudType,
			"limit":     customControlsPageSize,
			"offset":    0,
		}
		if err := c.Execute(ctx, customControlsQuery, vars, &out); err != nil {
			return nil, err
		}
		for _, row := range out.CloudControls.Items {
			if row.custom() {
				controls = append(controls, row.control())
			}
		}
	}
	sort.Slice(controls, func(i, j int) bool { return controls[i].Key < controls[j].Key })
	return controls, nil
}

func validateCustomControlCloud(cloudType string) error {
	for _, known := range CustomControlCloudTypes {
		if cloudType == known {
			return nil
		}
	}
	return fmt.Errorf("custom controls are not available for cloud type %q; want one of %s",
		cloudType, strings.Join(CustomControlCloudTypes, ", "))
}
