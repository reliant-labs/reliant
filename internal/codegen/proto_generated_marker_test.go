// File: internal/codegen/proto_generated_marker_test.go
//
// Pins `// forge:generated <expr>` in the raw scanner: the expression is the
// rest of the marker's line, both placements carry it, the field is
// read-only, and every marker leaves a site ledger entry birth can name when
// it refuses the expression.

package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedMarkerExpr(t *testing.T) {
	cases := []struct {
		comment   string
		wantExpr  string
		wantFound bool
	}{
		// Raw source spellings (the scanner hands over the `//` comment).
		{"// forge:generated round(quantity * unit_price_cents)::BIGINT", "round(quantity * unit_price_cents)::BIGINT", true},
		{"//forge:generated a - b", "a - b", true},
		{"// forge:generated   (a + b) / 2   ", "(a + b) / 2", true},
		// Descriptor spelling: buf stripped the slashes; any line of a block.
		{" The balance.\n forge:generated amount_cents - amount_paid_cents\n", "amount_cents - amount_paid_cents", true},
		// The marker with nothing after it: found, no expression — refused at birth.
		{"// forge:generated", "", true},
		{"// forge:generated   ", "", true},
		// No blank after the token: still the marker (it leaves the write
		// surface) but no expression is recovered — refused, never guessed.
		{"// forge:generated(a + b)", "", true},
		// Not the marker.
		{"// forge:read-only", "", false},
		{"// forge:generatedish a", "", false},
		{"// see forge:generated docs", "", false},
	}
	for _, c := range cases {
		expr, found := GeneratedMarkerExpr(c.comment)
		if expr != c.wantExpr || found != c.wantFound {
			t.Errorf("GeneratedMarkerExpr(%q) = (%q, %v), want (%q, %v)", c.comment, expr, found, c.wantExpr, c.wantFound)
		}
	}
}

const generatedMarkerProto = `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message LineItem {
  double quantity = 1;
  int64 unit_price_cents = 2 [(buf.validate.field).int64.gte = 0];

  int64 line_total_cents = 3; // forge:generated round(quantity * unit_price_cents)::BIGINT

  // What is left to pay.
  // forge:generated amount_cents - amount_paid_cents
  int64 balance_cents = 4;

  // forge:read-only
  // forge:generated unit_price_cents * 2
  int64 doubled_cents = 5;

  int64 broken_cents = 6; // forge:generated

  string note = 7;
}
`

func TestRawScan_GeneratedMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "estimates.proto")
	if err := os.WriteFile(path, []byte(generatedMarkerProto), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	m, ok := scan.MessageByName("LineItem")
	if !ok {
		t.Fatal("LineItem not scanned")
	}
	byName := map[string]SchemaFieldDef{}
	for _, f := range m.Fields {
		byName[f.Name] = f
	}

	for name, want := range map[string]string{
		"line_total_cents": "round(quantity * unit_price_cents)::BIGINT", // trailing, after inline options' sibling
		"balance_cents":    "amount_cents - amount_paid_cents",           // leading, below a prose line
		"doubled_cents":    "unit_price_cents * 2",                       // stacked under forge:read-only
		"broken_cents":     "",                                           // no expression
	} {
		f := byName[name]
		if f.Generated != want {
			t.Errorf("%s.Generated = %q, want %q", name, f.Generated, want)
		}
		if !f.ReadOnly {
			t.Errorf("%s must be read-only — postgres refuses any write to a generated column", name)
		}
	}
	for _, name := range []string{"quantity", "unit_price_cents", "note"} {
		if byName[name].Generated != "" || byName[name].ReadOnly {
			t.Errorf("%s is unmarked and must stay a plain writable field: %+v", name, byName[name])
		}
	}

	// Stacked leading markers both bind to the field below them: neither is
	// reported as attaching to no field.
	if len(m.UnappliedReadOnlyMarkers) != 0 {
		t.Errorf("every marker bound to a field, got unapplied: %+v", m.UnappliedReadOnlyMarkers)
	}

	// The site ledger: one entry per marker, with the line the author fixes.
	wantSites := map[string]int{"line_total_cents": 10, "balance_cents": 13, "doubled_cents": 17, "broken_cents": 20}
	if len(m.GeneratedMarkers) != len(wantSites) {
		t.Fatalf("GeneratedMarkers = %+v, want %d entries", m.GeneratedMarkers, len(wantSites))
	}
	for _, gm := range m.GeneratedMarkers {
		if line, ok := wantSites[gm.Field]; !ok || gm.Site.Line != line || gm.Site.File != path {
			t.Errorf("marker for %s at %s:%d, want line %d of %s", gm.Field, gm.Site.File, gm.Site.Line, line, path)
		}
		if gm.Field == "broken_cents" && gm.Expr != "" {
			t.Errorf("broken_cents carries no expression, got %q", gm.Expr)
		}
	}
}

// TestGeneratedMarkerIsKnownAndReadOnly pins registry membership: an
// unregistered marker warns as a typo on every use, and one outside the
// read-only set would leave a generated field on the Create request — a
// client-writable value postgres rejects on insert.
func TestGeneratedMarkerIsKnownAndReadOnly(t *testing.T) {
	if !IsKnownProtoMarker(ProtoMarkerGenerated) {
		t.Fatalf("%s must be in KnownProtoMarkers", ProtoMarkerGenerated)
	}
	readOnly := false
	for _, m := range ReadOnlyProtoMarkers {
		if m == ProtoMarkerGenerated {
			readOnly = true
		}
	}
	if !readOnly {
		t.Errorf("%s must be in ReadOnlyProtoMarkers", ProtoMarkerGenerated)
	}
}
