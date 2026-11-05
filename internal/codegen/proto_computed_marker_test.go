// File: internal/codegen/proto_computed_marker_test.go
//
// Pins that `// forge:computed` is READ-ONLY in the raw scanner, not a
// token only lint understands. A marker that changed no generated output
// would leave a "computed" field still client-writable on Create — the
// exact silent-write hole read-only exists to close — while reading, in
// the proto, as though it had been closed.

package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

const computedMarkerProto = `syntax = "proto3";

package services.estimates.v1;

// forge:entity
message EstimateLineItem {
  string description = 1;
  int64 quantity_milli = 2;
  int64 unit_price_cents = 3;

  // quantity_milli * unit_price_cents / 1000, maintained on write.
  int64 amount_cents = 4; // forge:computed

  // forge:computed
  int64 margin_cents = 5;

  int64 legacy_total_cents = 6; // forge:read-only

  string id = 7;
}
`

// TestComputedMarkerImpliesReadOnly checks both accepted spellings — a
// trailing inline marker and a full-line one preceding the field — set
// SchemaFieldDef.ReadOnly, and that an unmarked field does not.
func TestComputedMarkerImpliesReadOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "estimates.proto"), []byte(computedMarkerProto), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	m, ok := scan.MessageByName("EstimateLineItem")
	if !ok {
		t.Fatal("EstimateLineItem not found")
	}
	readOnly := map[string]bool{}
	for _, f := range m.Fields {
		readOnly[f.Name] = f.ReadOnly
	}

	for _, name := range []string{"amount_cents", "margin_cents", "legacy_total_cents"} {
		if !readOnly[name] {
			t.Errorf("%s must be read-only — a computed field forge still accepts on Create "+
				"is client-writable, which is what the marker is for", name)
		}
	}
	for _, name := range []string{"description", "quantity_milli", "unit_price_cents"} {
		if readOnly[name] {
			t.Errorf("%s is unmarked and must stay client-writable", name)
		}
	}

	// A marker every field consumed leaves no unapplied sites: birth
	// refuses an entity carrying a marker it could not honour, so a
	// computed marker that the field-binding logic failed to attach
	// would surface here rather than being silently dropped.
	if len(m.UnappliedReadOnlyMarkers) != 0 {
		t.Errorf("every marker should have bound to a field, got unapplied: %+v", m.UnappliedReadOnlyMarkers)
	}
}

// TestComputedMarkerIsKnown pins registry membership: an unregistered
// marker is reported as a typo by the unknown-proto-marker lint check,
// which would make the recommended spelling warn on every use.
func TestComputedMarkerIsKnown(t *testing.T) {
	if !IsKnownProtoMarker(ProtoMarkerComputed) {
		t.Fatalf("%s must be in KnownProtoMarkers", ProtoMarkerComputed)
	}
	// Exactness, matching the discipline the rest of the registry keeps:
	// a longer token that merely starts with the marker is not it.
	if IsKnownProtoMarker("forge:computed-ish") {
		t.Error("marker matching must stay exact")
	}
}
