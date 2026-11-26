package codegen

import (
	"strings"
	"testing"
)

// `// forge:owner` on a field declares, at birth, the column naming who a
// row belongs to. Before it there was no proto-side spelling at all: the
// owner could only be declared by a later migration, which always met a
// handlers_crud.go that already existed, so the scoping wrapper was never
// scaffolded on the normal path.
func TestScanRawProtoDir_OwnerMarker(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/agents.proto": `syntax = "proto3";
package services.agents.v1;

// forge:entity
message AgentRun {
  string id = 1;
  // forge:read-only forge:owner
  string tenant_id = 2;
  string name = 3;
  // the forge:owner of a run is its tenant (prose, not a marker)
  string note = 4;
}

// forge:entity
message Tenant {
  string id = 1; // forge:owner
  string slug = 2;
}

// forge:entity
message Workspace {
  string id = 1;
  string org_id = 2; // forge:read-only forge:owner
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	type flags struct{ owner, readOnly bool }
	want := map[string]map[string]flags{
		"AgentRun":  {"id": {}, "tenant_id": {owner: true, readOnly: true}, "name": {}, "note": {}},
		"Tenant":    {"id": {owner: true}, "slug": {}},
		"Workspace": {"id": {}, "org_id": {owner: true, readOnly: true}},
	}
	for msg, fields := range want {
		m, ok := scan.MessageByName(msg)
		if !ok {
			t.Fatalf("%s not scanned", msg)
		}
		if len(m.UnappliedOwnerMarkers) != 0 {
			t.Errorf("%s: every owner marker attaches to a field, got unapplied %v", msg, m.UnappliedOwnerMarkers)
		}
		for _, f := range m.Fields {
			if got := (flags{f.Owner, f.ReadOnly}); got != fields[f.Name] {
				t.Errorf("%s.%s: (owner, read-only) = %+v, want %+v", msg, f.Name, got, fields[f.Name])
			}
		}
	}
}

// A marker that attaches to no field is reported, never dropped: the
// author believes the table is owned, and birth would scaffold it unscoped.
func TestScanRawProtoDir_UnappliedOwnerMarker(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/agents.proto": `syntax = "proto3";
package services.agents.v1;

// forge:entity
message AgentRun {
  string id = 1;
  string name = 2;
  // forge:owner
  reserved 3;
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	m, _ := scan.MessageByName("AgentRun")
	if len(m.UnappliedOwnerMarkers) != 1 || !strings.Contains(m.UnappliedOwnerMarkers[0].String(), "agents.proto:8: // forge:owner") {
		t.Fatalf("UnappliedOwnerMarkers = %v, want the one at agents.proto:8", m.UnappliedOwnerMarkers)
	}
	for _, f := range m.Fields {
		if f.Owner {
			t.Errorf("field %s took a marker that was not above it", f.Name)
		}
	}
}

// A marker is scoped to the message body it sits in: one closing a body
// never leaks into the next message's first field, and one outside every
// body — above a message, as if it declared table ownership — is refused,
// attributed to the message it precedes, rather than ignored.
func TestScanRawProtoDir_OwnerMarkerOutsideFieldIsRefused(t *testing.T) {
	dir := writeRawScanFixture(t, map[string]string{"v1/agents.proto": `syntax = "proto3";
package services.agents.v1;

// forge:entity forge:owner
message AgentRun {
  string id = 1;
  string tenant_id = 2;
  // forge:owner
}

// forge:owner

// forge:entity
message Tenant {
  string owner_id = 1;
  string slug = 2;
}
`})
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	want := map[string][]string{
		"AgentRun": {"agents.proto:4: // forge:entity forge:owner", "agents.proto:8: // forge:owner"},
		"Tenant":   {"agents.proto:11: // forge:owner"},
	}
	for msg, sites := range want {
		m, ok := scan.MessageByName(msg)
		if !ok {
			t.Fatalf("%s not scanned", msg)
		}
		if len(m.UnappliedOwnerMarkers) != len(sites) {
			t.Fatalf("%s: UnappliedOwnerMarkers = %v, want %v", msg, m.UnappliedOwnerMarkers, sites)
		}
		for i, s := range sites {
			if got := m.UnappliedOwnerMarkers[i].String(); !strings.Contains(got, s) {
				t.Errorf("%s: unapplied[%d] = %q, want it to name %q", msg, i, got, s)
			}
		}
		for _, f := range m.Fields {
			if f.Owner {
				t.Errorf("%s.%s took a marker that was not on or directly above it", msg, f.Name)
			}
		}
	}
}

// The descriptor path reads comment text buf already stripped the slashes
// from, by the same grammar.
func TestOwnerMarkerCommentRE(t *testing.T) {
	for comment, want := range map[string]bool{
		" forge:owner\n":                          true,
		" forge:read-only forge:owner\n":          true,
		" forge:fill=handler forge:owner — tid\n": true,
		" Tenant scope.\n forge:owner\n":          true,
		" the forge:owner column\n":               false,
		" forge:owner-ish\n":                      false,
		" forge:owners\n":                         false,
	} {
		if got := OwnerMarkerCommentRE.MatchString(comment); got != want {
			t.Errorf("OwnerMarkerCommentRE(%q) = %v, want %v", comment, got, want)
		}
	}
	if !IsKnownProtoMarker(ProtoMarkerOwner) {
		t.Errorf("%s must be in KnownProtoMarkers, or the proto-markers lint calls every use of it unknown", ProtoMarkerOwner)
	}
}
