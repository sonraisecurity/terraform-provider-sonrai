package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

func transactionSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	NewControlTransactionDataSource().Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %s", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("invalid schema: %s", diags)
	}
	return resp.Schema
}

func TestControlTransactionSchemaIsValid(t *testing.T) {
	transactionSchema(t)
}

func TestControlTransactionAttributes(t *testing.T) {
	attrs := transactionSchema(t).Attributes

	if attr, ok := attrs["transaction_id"]; !ok || !attr.IsRequired() {
		t.Error("transaction_id should be required")
	}
	for _, name := range []string{"wait_for", "wait_timeout"} {
		if attr, ok := attrs[name]; !ok || !attr.IsOptional() {
			t.Errorf("%s should be optional", name)
		}
	}
	for _, name := range []string{"id", "root_scope", "status", "status_message", "template_location"} {
		attr, ok := attrs[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
			t.Errorf("attribute %q should be computed-only", name)
		}
	}
}

func TestControlTransactionTypeName(t *testing.T) {
	resp := &datasource.MetadataResponse{}
	NewControlTransactionDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "sonrai"}, resp)

	if want := "sonrai_control_transaction"; resp.TypeName != want {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, want)
	}
}

func TestParseDurationOr(t *testing.T) {
	cases := map[string]struct {
		value, fallback string
		want            time.Duration
	}{
		"parses a value":           {"5m", "30m", 5 * time.Minute},
		"falls back when unset":    {"", "30m", 30 * time.Minute},
		"falls back when garbage":  {"soon", "30m", 30 * time.Minute},
		"falls back when zero":     {"0s", "30m", 30 * time.Minute},
		"falls back when negative": {"-5m", "30m", 30 * time.Minute},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := parseDurationOr(tc.value, tc.fallback); got != tc.want {
				t.Errorf("parseDurationOr(%q, %q) = %s, want %s", tc.value, tc.fallback, got, tc.want)
			}
		})
	}
}
