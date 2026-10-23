// File: internal/codegen/schema_hardening_test.go
//
// Unit tests for the codegen half of the schema-hardening markers:
//   - the raw scan capturing the `// forge:append-only` / `// forge:soft-delete`
//     entity markers,
//   - the conversion generator skipping a `// forge:secret` field on the
//     READ path while keeping it writable, and
//   - the secret field dropping out of the generated list-search span.

package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRawProtoDir_BehaviorMarkers(t *testing.T) {
	dir := t.TempDir()
	proto := `syntax = "proto3";
package svc.v1;

// forge:append-only
message AuditLog {
  string id = 1;
  string action = 2;
}

// forge:soft-delete
message Session {
  string id = 1;
}

// forge:entity
message Order {
  string id = 1;
}

message Plain {
  string id = 1;
}
`
	if err := os.WriteFile(filepath.Join(dir, "svc.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	by := map[string]RawProtoMessage{}
	for _, m := range scan.Messages {
		by[m.Name] = m
	}

	if a := by["AuditLog"]; !a.Marked || !a.AppendOnly || a.SoftDeleteMarked {
		t.Errorf("AuditLog: Marked=%v AppendOnly=%v SoftDelete=%v — want Marked+AppendOnly only", a.Marked, a.AppendOnly, a.SoftDeleteMarked)
	}
	if s := by["Session"]; !s.Marked || !s.SoftDeleteMarked || s.AppendOnly {
		t.Errorf("Session: Marked=%v AppendOnly=%v SoftDelete=%v — want Marked+SoftDelete only", s.Marked, s.AppendOnly, s.SoftDeleteMarked)
	}
	if o := by["Order"]; !o.Marked || o.AppendOnly || o.SoftDeleteMarked {
		t.Errorf("Order: Marked=%v AppendOnly=%v SoftDelete=%v — want Marked only", o.Marked, o.AppendOnly, o.SoftDeleteMarked)
	}
	if p := by["Plain"]; p.Marked || p.AppendOnly || p.SoftDeleteMarked {
		t.Errorf("Plain (no marker) must carry no marker flags, got %+v", p)
	}
}

// A marker must not leak onto the NEXT message when a blank/comment line
// sits between marker and declaration, and must be consumed by exactly one
// message.
func TestScanRawProtoDir_MarkerDoesNotLeak(t *testing.T) {
	dir := t.TempDir()
	proto := `syntax = "proto3";
package svc.v1;

// forge:append-only

// a doc comment about the ledger
message Ledger { string id = 1; }

message After { string id = 1; }
`
	if err := os.WriteFile(filepath.Join(dir, "svc.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]RawProtoMessage{}
	for _, m := range scan.Messages {
		by[m.Name] = m
	}
	if !by["Ledger"].AppendOnly {
		t.Error("marker must survive the blank + doc-comment lines and reach Ledger")
	}
	if by["After"].AppendOnly || by["After"].Marked {
		t.Error("marker must be consumed by Ledger, not leak onto After")
	}
}

func TestBuildEntityConv_SecretFieldSkipsReadPath(t *testing.T) {
	svc := ServiceDef{Name: "ShopService", Package: "svc.v1"}
	entity := EntityDef{
		Name: "Credential",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", Kind: FieldKindScalar, GoType: "string"},
			{Name: "name", GoName: "Name", Kind: FieldKindScalar, GoType: "string"},
			{Name: "secret_token", GoName: "SecretToken", Kind: FieldKindScalar, GoType: "string", Secret: true},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "name", Type: "string", NotNull: true},
			{Name: "secret_token", Type: "string", NotNull: true},
		},
	}
	conv, _ := BuildEntityConv(svc, entity)

	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	// Read path: the secret is NOT packed — only an explanatory comment.
	if strings.Contains(toProto, "m.SecretToken = ") {
		t.Errorf("secret field must not be packed onto the wire (toProto):\n%s", toProto)
	}
	if !strings.Contains(toProto, "SecretToken: forge:secret") {
		t.Errorf("toProto should carry the forge:secret skip note:\n%s", toProto)
	}
	// Write path: the secret IS mapped (settable on create/update).
	if !strings.Contains(fromProto, "e.SecretToken = m.SecretToken") {
		t.Errorf("secret field must stay writable (fromProto):\n%s", fromProto)
	}
	// Non-secret fields still pack.
	if !strings.Contains(toProto, "m.Name = e.Name") {
		t.Errorf("non-secret field must still pack:\n%s", toProto)
	}
}

func TestDropSecretSearchColumns(t *testing.T) {
	fields := []EntityField{
		{Name: "name"},
		{Name: "secret_token", Secret: true},
		{Name: "notes"},
	}
	got := dropSecretSearchColumns([]string{"name", "secret_token", "notes"}, fields)
	want := []string{"name", "notes"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dropSecretSearchColumns = %v, want %v", got, want)
	}
	// No secret fields → unchanged.
	plain := dropSecretSearchColumns([]string{"a", "b"}, []EntityField{{Name: "a"}, {Name: "b"}})
	if strings.Join(plain, ",") != "a,b" {
		t.Errorf("no-secret case must be a no-op, got %v", plain)
	}
}
