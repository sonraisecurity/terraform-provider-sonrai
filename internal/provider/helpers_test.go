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

func TestDifference(t *testing.T) {
	// Used to work out which exemptions to remove from a protect: anything in
	// the first set and not the second must come back, sorted.
	current := []string{"bedrock", "cloud9", "guardduty"}
	desired := []string{"cloud9", "sagemaker"}

	add := difference(desired, current)
	remove := difference(current, desired)

	if len(add) != 1 || add[0] != "sagemaker" {
		t.Errorf("add = %v, want [sagemaker]", add)
	}
	if len(remove) != 2 || remove[0] != "bedrock" || remove[1] != "guardduty" {
		t.Errorf("remove = %v, want [bedrock guardduty]", remove)
	}
}

func TestDifferenceEmptyCases(t *testing.T) {
	if got := difference(nil, []string{"a"}); len(got) != 0 {
		t.Errorf("difference(nil, …) = %v, want empty", got)
	}
	if got := difference([]string{"a", "b"}, nil); len(got) != 2 {
		t.Errorf("difference(…, nil) = %v, want both entries", got)
	}
}

func TestSetToStringsSorts(t *testing.T) {
	ctx := context.Background()
	set, diags := stringsToSet(ctx, []string{"sagemaker", "cloud9"})
	if diags.HasError() {
		t.Fatalf("stringsToSet: %s", diags)
	}
	got, diags := setToStrings(ctx, set)
	if diags.HasError() {
		t.Fatalf("setToStrings: %s", diags)
	}
	if len(got) != 2 || got[0] != "cloud9" || got[1] != "sagemaker" {
		t.Errorf("got %v, want sorted [cloud9 sagemaker]", got)
	}
}
