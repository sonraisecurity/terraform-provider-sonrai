package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/sonraisecurity/terraform-provider-sonrai/internal/client"
)

func submissionSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewControlSubmissionResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestControlSubmissionSchemaIsValid(t *testing.T) {
	submissionSchema(t)
}

func TestControlSubmissionAttributes(t *testing.T) {
	attrs := submissionSchema(t).Attributes

	if attr, ok := attrs["root_scope"]; !ok || !attr.IsRequired() {
		t.Error("root_scope should be required")
	}
	if attr, ok := attrs["triggers"]; !ok || !attr.IsOptional() {
		t.Error("triggers should be optional")
	}

	for _, name := range []string{"transaction_id", "status", "status_message", "template_location"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

func TestControlSubmissionTypeName(t *testing.T) {
	resp := &resource.MetadataResponse{}
	NewControlSubmissionResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_control_submission"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestDurationValidator(t *testing.T) {
	for _, value := range []string{"15m", "1h30m", "90s"} {
		if rejectsDuration(value) {
			t.Errorf("%q should be accepted", value)
		}
	}
	// "15" has no unit, and a non-positive timeout would make waiting meaningless.
	for _, value := range []string{"", "soon", "15", "-5m", "0s"} {
		if !rejectsDuration(value) {
			t.Errorf("%q should be rejected", value)
		}
	}
}

// rejectsDuration reports whether durationValidator refused value.
func rejectsDuration(value string) bool {
	req := validator.StringRequest{
		Path:        path.Root("wait_timeout"),
		ConfigValue: types.StringValue(value),
	}
	resp := &validator.StringResponse{}
	durationValidator{}.ValidateString(context.Background(), req, resp)
	return resp.Diagnostics.HasError()
}

// TestSubmissionCancelledPredicate pins which transaction statuses mean the
// submission still stands. A cancelled one returns its changes to staged, so
// the resource must not survive a refresh -- otherwise the staged changes sit
// unsubmitted with no diff to prompt another attempt.
func TestSubmissionCancelledPredicate(t *testing.T) {
	cases := map[string]bool{
		client.StatusCancelled: true,
		"CANCELED":             true,
		client.StatusCompleted: false,
		client.StatusFailed:    false,
		// Any non-terminal status, whatever the platform happens to call it.
		"SOME_INTERMEDIATE_STATUS": false,
	}
	for status, wantRemoved := range cases {
		if got := submissionCancelled(status); got != wantRemoved {
			t.Errorf("status %q removed=%v, want %v", status, got, wantRemoved)
		}
	}
}

// TestControlSubmissionHasNoPresignedURL pins the omission. The platform signs
// a fresh URL on every read, so an attribute holding one reports drift on
// every plan and no amount of applying settles it.
func TestControlSubmissionHasNoPresignedURL(t *testing.T) {
	if _, ok := submissionSchema(t).Attributes["presigned_template_source"]; ok {
		t.Error("presigned_template_source is unstable and must not be stored on the resource")
	}
}

// TestControlTransactionHasNoPresignedURL pins the same omission on the data
// source: the URL is a short-lived bearer credential, and a data source result
// persists in state exactly like a resource attribute.
func TestControlTransactionHasNoPresignedURL(t *testing.T) {
	if _, ok := transactionSchema(t).Attributes["presigned_template_source"]; ok {
		t.Error("presigned_template_source is a credential and must not reach state")
	}
}
