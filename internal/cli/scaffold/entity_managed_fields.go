// File: internal/cli/scaffold/entity_managed_fields.go
//
// Managed-field injection — the birth-time proto edit that makes a
// hand-authored entity message DECLARE its own identity and lifecycle
// fields.
//
// forge's generated Go entity, its ORM row type, the born CRUD test, the
// TypeScript client and every scaffolded edit page all reference `id`
// (and created_at/updated_at). Those fields used to exist by CONVENTION:
// the proto never declared them, the skill told authors never to declare
// them, and every emitter assumed them anyway — so a by-the-book scaffold
// produced a message with no `id` and a test calling `GetId()` on it. A
// field that exists without being declared anywhere is not a convention,
// it is a hidden contract, and this file deletes it.
//
// Birth injects the managed fields into the author's message, in the
// author's file, exactly as quintet completion injects the CRUD RPCs
// (entity_quintet.go) — one-time, at birth, never reconciled afterwards.
// From then on the proto is the whole truth: what you read is what is
// generated.
//
// Two invariants make the edit safe to run over a file someone else owns:
//
//   - IDEMPOTENT. A field the message already declares is left exactly as
//     written — never duplicated, never retyped. A message that declares
//     all of them is a clean no-op, so re-running birth (or hand-writing
//     the fields, as the old convention forced people to) costs nothing.
//   - NEVER RENUMBERS. Injected fields take numbers above the message's
//     high-water mark (RawProtoMessage.MaxFieldNumber, which counts
//     `reserved` too). Renumbering an existing field is a wire-breaking
//     change and would be a far worse defect than the one this fixes.

package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// managedEntityField is one field forge injects at birth: the proto
// declaration plus the import it needs.
type managedEntityField struct {
	name  string
	proto string
	// needsTimestampImport marks the google.protobuf.Timestamp fields.
	needsTimestampImport bool
}

// managedEntityFields returns the fields an entity of this shape is born
// with, in declaration order. It mirrors exactly what the birth migration
// emits (internal/scaffold.RenderEntityMigrationFromProto): the TEXT
// primary key, the managed timestamps when they are on, and deleted_at
// only when soft delete is on — never a field the table will not have.
func managedEntityFields(timestamps, softDelete bool) []managedEntityField {
	out := []managedEntityField{{name: "id", proto: "string"}}
	if timestamps {
		out = append(out,
			managedEntityField{name: "created_at", proto: "google.protobuf.Timestamp", needsTimestampImport: true},
			managedEntityField{name: "updated_at", proto: "google.protobuf.Timestamp", needsTimestampImport: true},
		)
	}
	if softDelete {
		out = append(out, managedEntityField{name: "deleted_at", proto: "google.protobuf.Timestamp", needsTimestampImport: true})
	}
	return out
}

// injectManagedEntityFields appends the managed fields `entity` is missing
// to the message that declares it, returning the field names it added
// (empty when the message already declared them all — the idempotent
// no-op). The message is re-located by a FRESH scan of its file, so a
// sweep that births several entities into one proto never edits at a
// stale offset.
func injectManagedEntityFields(protoPath, entity string, timestamps, softDelete bool) ([]string, error) {
	m, err := rescanMessage(protoPath, entity)
	if err != nil {
		return nil, err
	}

	declared := make(map[string]bool, len(m.Fields))
	for _, f := range m.Fields {
		declared[f.Name] = true
	}
	var missing []managedEntityField
	for _, mf := range managedEntityFields(timestamps, softDelete) {
		if !declared[mf.name] {
			missing = append(missing, mf)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}

	raw, err := os.ReadFile(protoPath)
	if err != nil {
		return nil, err
	}
	content := string(raw)
	if m.BodyClose <= 0 || m.BodyClose > len(content) || content[m.BodyClose] != '}' {
		return nil, fmt.Errorf("could not locate the body of message %s in %s", entity, filepath.Base(protoPath))
	}

	var b strings.Builder
	b.WriteString("  // Managed by forge at birth: the identity and lifecycle fields the\n")
	b.WriteString("  // migration, the ORM row type, the CRUD envelopes and the frontend\n")
	b.WriteString("  // all project. Yours from here — forge never rewrites them.\n")
	num := m.MaxFieldNumber
	added := make([]string, 0, len(missing))
	needsTimestamp := false
	for _, mf := range missing {
		num++
		fmt.Fprintf(&b, "  %s %s = %d;\n", mf.proto, mf.name, num)
		added = append(added, mf.name)
		needsTimestamp = needsTimestamp || mf.needsTimestampImport
	}

	head := strings.TrimRight(content[:m.BodyClose], " \t")
	if !strings.HasSuffix(head, "\n") {
		head += "\n" // a one-line body (`message W { string a = 1; }`)
	}
	content = head + b.String() + content[m.BodyClose:]
	if needsTimestamp {
		content = ensureProtoImport(content, "google/protobuf/timestamp.proto")
	}
	if err := atomicfile.WriteFile(protoPath, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return added, nil
}

// rescanMessage re-reads the message's declaring directory and returns the
// message as it is on disk RIGHT NOW — body offsets and field high-water
// mark included. Every caller edits proto text, so nothing may be trusted
// from an earlier scan.
func rescanMessage(protoPath, entity string) (codegen.RawProtoMessage, error) {
	scan, err := codegen.ScanRawProtoDir(filepath.Dir(protoPath))
	if err != nil {
		return codegen.RawProtoMessage{}, err
	}
	for _, m := range scan.Messages {
		if m.Name == entity && m.File == protoPath {
			return m, nil
		}
	}
	return codegen.RawProtoMessage{}, fmt.Errorf("message %s is not declared in %s", entity, filepath.Base(protoPath))
}

// entityDeclFileFor resolves the file declaring the entity message, "" when
// the raw scan never saw it (entity declared outside the service's proto
// dir — the injection is then skipped with a note, exactly like quintet
// completion).
func entityDeclFileFor(scan *codegen.RawProtoScan, entity string) string {
	if scan == nil {
		return ""
	}
	if m, ok := scan.MessageByName(entity); ok {
		return m.File
	}
	return ""
}
