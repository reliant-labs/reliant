// File: internal/cli/lint/lint_unwritten_outcome_test.go
//
// Tests for the consequence clause both unwritten-column rules splice into
// their fix hints.
//
// The defect these pin: the clause used to be a FIXED money illustration
// ("for a money column that ships as $0.00"), spliced into every finding
// regardless of the column's actual type. A TIMESTAMPTZ finding therefore
// described itself as a money column, and a dogfood run's agent read it as
// forge having mis-detected the column as numeric and went looking for a
// type error in the proto that was not there.
//
// An illustration and a finding must not share a sentence when the
// illustration names a type the field does not have. So the consequence is
// now derived from the type forge already parsed, and these tests pin both
// directions: a money column still gets the vivid $0.00 that made this
// warning work, and a non-money column never claims to be one.

package lint

import (
	"strings"
	"testing"
)

func TestShapeConsequence_MoneyKeepsTheDollarsAndCents(t *testing.T) {
	got := shapeConsequence(shapeMoney)
	if !strings.Contains(got, "$0.00") {
		t.Errorf("a money column must keep the vivid consequence: %q", got)
	}
}

// TestShapeConsequence_NonNumericNeverClaimsMoney is the reported bug,
// pinned at the smallest unit: no shape other than money may mention
// dollars, and none of them may call the column a money column.
func TestShapeConsequence_NonNumericNeverClaimsMoney(t *testing.T) {
	for _, shape := range []valueShape{
		shapeUnknown, shapeNumeric, shapeText, shapeBool, shapeTimestamp, shapeJSON,
	} {
		got := shapeConsequence(shape)
		if strings.Contains(got, "$0.00") || strings.Contains(got, "money") {
			t.Errorf("shape %d describes itself as money: %q", shape, got)
		}
		if got == "" {
			t.Errorf("shape %d has no consequence at all — the warning's whole force is the consequence", shape)
		}
	}
}

func TestSQLValueShape(t *testing.T) {
	cases := []struct {
		sqlType, column string
		want            valueShape
	}{
		{"BIGINT", "total_cents", shapeMoney},
		{"NUMERIC(12, 2)", "unit_price", shapeMoney},
		{"BIGINT", "retry_count", shapeNumeric},
		// `total_items` is the near-miss that matters: a bare "total" in
		// the name is NOT evidence of money, and reading it as money
		// would reintroduce exactly the defect this file pins.
		{"INTEGER", "total_items", shapeNumeric},
		{"TIMESTAMPTZ", "quarantined_at", shapeTimestamp},
		{"TIMESTAMP WITH TIME ZONE", "approved_at", shapeTimestamp},
		{"DATE", "due_on", shapeTimestamp},
		{"BOOLEAN", "is_approved", shapeBool},
		{"TEXT", "summary", shapeText},
		{"VARCHAR(255)", "slug", shapeText},
		{"JSONB", "metadata", shapeJSON},
		{"UUID", "external_id", shapeUnknown},
		{"", "whatever", shapeUnknown},
	}
	for _, c := range cases {
		if got := sqlValueShape(c.sqlType, c.column); got != c.want {
			t.Errorf("sqlValueShape(%q, %q) = %d, want %d", c.sqlType, c.column, got, c.want)
		}
	}
}

func TestProtoValueShape(t *testing.T) {
	cases := []struct {
		kind, typeName, field string
		want                  valueShape
	}{
		{"int64", "", "amount_cents", shapeMoney},
		{"int64", "", "quantity_milli", shapeNumeric},
		{"bool", "", "is_approved", shapeBool},
		{"string", "", "summary", shapeText},
		{"message", "google.protobuf.Timestamp", "approved_at", shapeTimestamp},
		// Forge's own convention: a `_at` field is a timestamp even when
		// the proto spells it `string`, which is how the scaffolded
		// entities declare them.
		{"string", "", "quarantined_at", shapeTimestamp},
		{"message", "shop.v1.Address", "address", shapeUnknown},
	}
	for _, c := range cases {
		if got := protoValueShape(c.kind, c.typeName, c.field); got != c.want {
			t.Errorf("protoValueShape(%q, %q, %q) = %d, want %d", c.kind, c.typeName, c.field, got, c.want)
		}
	}
}
