package provider

import (
	"context"
	"testing"
)

func TestStringsToSetNilIsEmptyNotNull(t *testing.T) {
	set, diags := stringsToSet(context.Background(), nil)
	if diags.HasError() {
		t.Fatalf("diags: %s", diags)
	}
	if set.IsNull() {
		t.Fatal("nil slice became a null set; it must be the empty set or an imported empty account never converges")
	}
	if n := len(set.Elements()); n != 0 {
		t.Fatalf("got %d elements, want 0", n)
	}
}
