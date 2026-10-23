// File: internal/cli/lint/lint_computed_fields_test.go
//
// Tests for forgeconv-computed-field-unwritten. Each case builds a small
// project on disk — proto tree plus Go source — because the check's whole
// job is to correlate the two, and a test that stubbed either side would
// not exercise the correlation.

package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// computedProject writes a project with one proto and an optional handler
// file, returning the project root.
func computedProject(t *testing.T, protoBody, handlerGo string) string {
	t.Helper()
	root := t.TempDir()
	protoDir := filepath.Join(root, "proto", "services", "estimates", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "estimates.proto"), []byte(protoBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if handlerGo != "" {
		handlerDir := filepath.Join(root, "internal", "handlers", "estimates")
		if err := os.MkdirAll(handlerDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(handlerDir, "handlers_crud.go"), []byte(handlerGo), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// lineItemProto is the motivating shape: a computed money column whose
// comment promises a derivation.
const lineItemProto = `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message EstimateLineItem {
  string description = 1;
  int64 quantity_milli = 2;
  int64 unit_price_cents = 3;

  // quantity_milli * unit_price_cents / 1000, maintained on write.
  int64 amount_cents = 4; // forge:computed

  string id = 5;
}
`

// TestComputedFields_TripsWhenNothingWrites is the defect this rule exists
// for: the marker declares an obligation and no app code meets it, so the
// insert takes the column default and the app shows $0.00.
func TestComputedFields_TripsWhenNothingWrites(t *testing.T) {
	root := computedProject(t, lineItemProto, `package estimates

// A handler that delegates and derives nothing.
func (s *Service) CreateEstimateLineItem() error {
	return nil
}
`)
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Field != "amount_cents" || f.GoField != "AmountCents" || f.Entity != "EstimateLineItem" {
		t.Errorf("wrong field flagged: %+v", f)
	}
	// The finding points at the proto declaration — where the obligation
	// was declared and where the author decides how to resolve it.
	if f.Line != 12 {
		t.Errorf("want the proto declaration line 12, got %d", f.Line)
	}
	hint := computedFieldFixHint(f)
	for _, want := range []string{"AmountCents", "forge:read-only", "handlers_crud.go"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q: %s", want, hint)
		}
	}
}

// TestComputedFields_TimestampIsNotDescribedAsMoney is this rule's half of
// the same defect its read-only twin carries: the consequence clause used
// to name a money column unconditionally, so a timestamp field was told it
// ships as $0.00. The two rules share the wording and must stay consistent.
func TestComputedFields_TimestampIsNotDescribedAsMoney(t *testing.T) {
	root := computedProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string id = 1;
  // forge:computed
  string quarantined_at = 2;
}
`, "package estimates\n")
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	hint := computedFieldFixHint(findings[0])
	for _, unwanted := range []string{"$0.00", "money"} {
		if strings.Contains(hint, unwanted) {
			t.Errorf("a timestamp field must not be described with %q:\n%s", unwanted, hint)
		}
	}
}

// TestComputedFields_MoneyKeepsTheVividConsequence pins that the motivating
// case — an amount_cents that nothing derives — still says $0.00.
func TestComputedFields_MoneyKeepsTheVividConsequence(t *testing.T) {
	root := computedProject(t, lineItemProto, "package estimates\n")
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(findings), findings)
	}
	hint := computedFieldFixHint(findings[0])
	if !strings.Contains(hint, "$0.00") {
		t.Errorf("an amount_cents field must still get the vivid money consequence:\n%s", hint)
	}
}

// TestComputedFields_SilentWhenHookWrites is the primary false-positive
// guard: the obligation IS met, so the rule must say nothing.
func TestComputedFields_SilentWhenHookWrites(t *testing.T) {
	root := computedProject(t, lineItemProto, `package estimates

func lineItemAmountCents(quantityMilli, unitPriceCents int64) int64 {
	return quantityMilli * unitPriceCents / 1000
}

func (s *Service) CreateEstimateLineItem() error {
	row := &LineItem{}
	row.AmountCents = lineItemAmountCents(row.QuantityMilli, row.UnitPriceCents)
	return nil
}
`)
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("rule fired on a field that IS computed — the write is right there:\n%+v", findings)
	}
}

// TestComputedFields_CompositeLiteralCounts pins that a constructor is as
// legitimate a derivation site as an assignment. Missing this shape would
// report correct code, which is how a rule earns a blanket disable.
func TestComputedFields_CompositeLiteralCounts(t *testing.T) {
	root := computedProject(t, lineItemProto, `package estimates

func newLineItem(qty, price int64) *LineItem {
	return &LineItem{
		AmountCents: qty * price / 1000,
	}
}
`)
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a composite-literal write must count as computing the field:\n%+v", findings)
	}
}

// TestComputedFields_GeneratedAndTestWritesDoNotCount is the rule's
// load-bearing exclusion. The generated conversion assigns EVERY field
// (`e.AmountCents = m.AmountCents`), so counting generated writes would
// mark every computed field satisfied and the check would never fire at
// all. A test factory setting the field proves nothing about production
// either.
func TestComputedFields_GeneratedAndTestWritesDoNotCount(t *testing.T) {
	root := computedProject(t, lineItemProto, "")
	handlerDir := filepath.Join(root, "internal", "handlers", "estimates")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The generated conversion — assigns the field, must not count.
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"), []byte(`package estimates

func toProto(e *LineItem, m *pb.LineItem) {
	m.AmountCents = e.AmountCents
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A test factory — assigns the field, must not count.
	if err := os.WriteFile(filepath.Join(handlerDir, "factories_test.go"), []byte(`package estimates

func factory() *LineItem {
	row := &LineItem{}
	row.AmountCents = 1234
	return row
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("generated/test writes must not satisfy the obligation, got %d findings: %+v",
			len(findings), findings)
	}
}

// TestComputedFields_LeadingMarkerPosition pins the second accepted marker
// position (a full-line comment above the field), so the check agrees with
// the scanner about what carries the marker.
func TestComputedFields_LeadingMarkerPosition(t *testing.T) {
	root := computedProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string title = 1;
  // forge:computed
  int64 total_cents = 2;
  string id = 3;
}
`, "package estimates\n")
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 || findings[0].Field != "total_cents" {
		t.Fatalf("a leading-line forge:computed marker must bind to the next field, got: %+v", findings)
	}
}

// TestComputedFields_PlainReadOnlyIsNotChecked pins the boundary between
// the two markers. forge:read-only carries NO derivation obligation — many
// read-only fields are legitimately written by a trigger or another
// service — so checking it would reproduce exactly the false-positive
// storm the explicit marker was chosen to avoid.
func TestComputedFields_PlainReadOnlyIsNotChecked(t *testing.T) {
	root := computedProject(t, `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message Estimate {
  string title = 1;
  // Totals in cents, maintained by RecalculateEstimate.
  int64 total_cents = 2; // forge:read-only
  string id = 3;
}
`, "package estimates\n")
	findings, err := collectComputedFieldFindings(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("forge:read-only carries no derivation obligation and must not be checked:\n%+v", findings)
	}
}

// TestComputedFields_NoProtoTreeIsClean pins that CLI/library projects,
// which have no proto tree, lint clean rather than erroring.
func TestComputedFields_NoProtoTreeIsClean(t *testing.T) {
	findings, err := collectComputedFieldFindings(t.TempDir())
	if err != nil {
		t.Fatalf("a project with no proto tree must not error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no findings, got %+v", findings)
	}
}
