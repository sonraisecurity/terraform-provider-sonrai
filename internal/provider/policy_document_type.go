package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var (
	_ basetypes.StringTypable                    = policyDocumentType{}
	_ basetypes.StringValuableWithSemanticEquals = policyDocument{}
)

// policyDocumentType is a string holding JSON. The platform stores a document
// compacted and with its keys reordered, so two values are the same when they
// decode to the same JSON, whatever their text.
type policyDocumentType struct {
	basetypes.StringType
}

func (t policyDocumentType) String() string {
	return "policyDocumentType"
}

func (t policyDocumentType) Equal(o attr.Type) bool {
	other, ok := o.(policyDocumentType)
	return ok && t.StringType.Equal(other.StringType)
}

func (t policyDocumentType) ValueType(context.Context) attr.Value {
	return policyDocument{}
}

func (t policyDocumentType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return policyDocument{StringValue: in}, nil
}

func (t policyDocumentType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	value, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	text, ok := value.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", value)
	}
	return policyDocument{StringValue: text}, nil
}

type policyDocument struct {
	basetypes.StringValue
}

func newPolicyDocument(text string) policyDocument {
	return policyDocument{StringValue: basetypes.NewStringValue(text)}
}

func newPolicyDocumentNull() policyDocument {
	return policyDocument{StringValue: basetypes.NewStringNull()}
}

func (v policyDocument) Type(context.Context) attr.Type {
	return policyDocumentType{}
}

func (v policyDocument) Equal(o attr.Value) bool {
	other, ok := o.(policyDocument)
	return ok && v.StringValue.Equal(other.StringValue)
}

func (v policyDocument) StringSemanticEquals(_ context.Context, o basetypes.StringValuable) (bool, diag.Diagnostics) {
	other, ok := o.(policyDocument)
	if !ok {
		var diags diag.Diagnostics
		diags.AddError("Unexpected value type",
			fmt.Sprintf("Expected a policy document, got %T. This is a bug in the provider.", o))
		return false, diags
	}
	return sameJSON(v.ValueString(), other.ValueString()), nil
}

// sameJSON reports whether two texts hold the same JSON value. Text that is
// not JSON is the same only as identical text, so an invalid document is never
// taken for an unchanged one.
func sameJSON(a, b string) bool {
	left, errA := decodeJSON(a)
	right, errB := decodeJSON(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return reflect.DeepEqual(left, right)
}

func decodeJSON(text string) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	// Numbers stay as written: decoding to float64 would call 1 and 1.0 the
	// same and lose digits past 2^53.
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return value, nil
}
