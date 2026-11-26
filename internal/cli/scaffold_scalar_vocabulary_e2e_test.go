//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// scaffold_scalar_vocabulary_e2e_test.go — the guard that makes the e2e
// corpus's type coverage DERIVED rather than remembered.
//
// The corpus's frontend lane (TestE2EScaffoldFrontendBuilds) is the only
// gate that reads generated TypeScript the way a user's build does:
// scaffold → generate → proto → TypeScript → `tsc --noEmit`. What
// it exercises is decided by two consts in scaffold_frontend_e2e_test.go
// (itemCRUDProto and itemsVocabularyMigration) — hand-written text.
//
// Hand-written text is a guard that cannot fail. For most of this
// corpus's life those consts carried 11 `string`, 4 `bool` and 1 `int64`
// and nothing else: zero bytes, int32, uint32, float, double. The unit
// layer DID know about `bytes` — schemadef_test.go pins BYTEA →
// TypeBytes, entityproto_test.go pins its SQL emission — so every unit
// test was green while a `bytes` column had never once travelled the full
// path. The defect lived in the COMPOSITION, which is the one thing an
// e2e corpus exists to catch, and it surfaced as 33 tsc errors in a
// generated frontend.
//
// Widening those consts fixes today and nothing else: the next kind added
// to the vocabulary is omitted in exactly the same silence. So the
// obligation is derived here instead, from codegen.ProtoScalarKinds() —
// the key set of protoScalarTS, the closed 15-kind table every frontend
// emitter projects through (frontend_ts_scalars.go). A kind in that table
// with no field in the corpus fails this test BY NAME.
//
// These tests read the corpus fixtures with forge's OWN proto scanner
// (codegen.ScanRawProtoDir) rather than grepping for type spellings. A
// substring search for "bytes" matches `f_bytes`, a comment, and the word
// "bytes" in prose; it is the same class of mistake as the hand-written
// list. The scanner reports the kind it CLASSIFIED each field as, which
// is the property the generator actually consumes downstream.

// vocabularyFixtureProtoDir materialises the corpus's proto fixture in a
// temp dir so the scanner can parse it. The const is the fixture; writing
// it to disk is just how the scanner takes input.
func vocabularyFixtureProtoDir(t *testing.T, proto string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "item.proto"), []byte(proto), 0o644); err != nil {
		t.Fatalf("write fixture proto: %v", err)
	}
	return dir
}

// scannedKinds returns, for the named message in the corpus fixture
// proto, the set of proto scalar kinds it declares — split by whether the
// field is singular or repeated. Both halves matter: five of the defect
// classes the vocabulary sweep found were repeated-only (`repeated bool`
// formed as string[], the repeated 64-bit integers formed as number[]),
// so a vocabulary that is complete singular and thin repeated reproduces
// the original gap in the other axis.
func scannedKinds(t *testing.T, protoDir, message string) (singular, repeated map[string]bool) {
	t.Helper()
	scan, err := codegen.ScanRawProtoDir(protoDir)
	if err != nil {
		t.Fatalf("scan corpus fixture proto: %v", err)
	}
	msg, ok := scan.MessageByName(message)
	if !ok {
		var names []string
		for _, m := range scan.Messages {
			names = append(names, m.Name)
		}
		t.Fatalf("corpus fixture proto declares no message %q (scanned: %v) — "+
			"the fixture was renamed and this guard is now reading nothing",
			message, names)
	}
	singular, repeated = map[string]bool{}, map[string]bool{}
	for _, f := range msg.Fields {
		if f.Repeated {
			repeated[f.Kind] = true
			continue
		}
		singular[f.Kind] = true
	}
	if len(singular) == 0 && len(repeated) == 0 {
		t.Fatalf("message %q scanned with ZERO fields — this guard would assert nothing and report green", message)
	}
	return singular, repeated
}

// requireClosedVocabulary is the shared assertion: every kind the closed
// table carries must appear in `got`, and the derived obligation must not
// be empty.
func requireClosedVocabulary(t *testing.T, what string, got map[string]bool) {
	t.Helper()
	kinds := codegen.ProtoScalarKinds()
	if len(kinds) == 0 {
		t.Fatal("codegen.ProtoScalarKinds() is EMPTY — the derived obligation is vacuous " +
			"and every assertion below would pass while covering nothing")
	}
	var missing []string
	for _, kind := range kinds {
		if !got[kind] {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%s covers %d of the %d proto scalar kinds; MISSING: %s\n\n"+
			"These kinds are in codegen's closed scalar table (protoScalarTS, "+
			"internal/codegen/frontend_ts_scalars.go), so every frontend emitter projects "+
			"them — but no field of that kind travels the end-to-end path, so nothing "+
			"checks what the projection emits.\n"+
			"That is exactly how `bytes` shipped green and produced 33 tsc errors in a "+
			"generated frontend.\n"+
			"Fix: add a field of each missing kind to itemCRUDProto (entity AND "+
			"CreateItemRequest) and a column to itemsVocabularyMigration, in "+
			"internal/cli/scaffold_frontend_e2e_test.go.",
			what, len(kinds)-len(missing), len(kinds), strings.Join(missing, ", "))
	}
}

// TestE2EVocabulary_EntityCarriesEveryScalarKind pins that the corpus
// entity — the message the list table, detail page, mock fixtures and
// hooks are all projected from — declares a field of every kind in the
// closed table, singular AND repeated.
//
// This test needs no scaffold, no npm and no network: it reads the same
// const TestE2EScaffoldFrontendBuilds writes to disk, so it fails in
// milliseconds and names the kind, rather than 150 seconds later as a tsc
// error inside a generated file.
func TestE2EVocabulary_EntityCarriesEveryScalarKind(t *testing.T) {
	t.Parallel()
	dir := vocabularyFixtureProtoDir(t, itemCRUDProto)
	singular, repeated := scannedKinds(t, dir, "Item")
	requireClosedVocabulary(t, "the corpus entity (Item, singular fields)", singular)
	requireClosedVocabulary(t, "the corpus entity (Item, repeated fields)", repeated)
}

// TestE2EVocabulary_CreateRequestCarriesEveryScalarKind pins the same for
// the create request. The two are genuinely different surfaces: the
// FORM is projected from the request message, so a kind present only on
// the entity is checked in table cells and mock fixtures but never in a
// zod schema or a mutate() payload — and four of the five defect classes
// the sweep found lived precisely there (z.string() feeding a Uint8Array
// field, number[] feeding bigint[]).
func TestE2EVocabulary_CreateRequestCarriesEveryScalarKind(t *testing.T) {
	t.Parallel()
	dir := vocabularyFixtureProtoDir(t, itemCRUDProto)
	singular, repeated := scannedKinds(t, dir, "CreateItemRequest")
	requireClosedVocabulary(t, "the corpus create request (CreateItemRequest, singular fields)", singular)
	requireClosedVocabulary(t, "the corpus create request (CreateItemRequest, repeated fields)", repeated)
}

// TestE2EVocabulary_EveryScalarFieldHasAColumn closes the loop on the
// SCHEMA half. Frontend CRUD pages are emitted only for entities whose
// table exists in the applied schema, and a wire field with no column is
// silently not converted — so a proto field whose column was forgotten
// travels no further than the proto. Without this, the two tests above
// could be satisfied by fields that generate nothing at all.
//
// The obligation is derived twice over: the FIELD NAMES come from the
// scanner (not from a list here), and each one must appear as a column in
// the migration the corpus applies.
func TestE2EVocabulary_EveryScalarFieldHasAColumn(t *testing.T) {
	t.Parallel()
	dir := vocabularyFixtureProtoDir(t, itemCRUDProto)
	scan, err := codegen.ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("scan corpus fixture proto: %v", err)
	}
	msg, ok := scan.MessageByName("Item")
	if !ok {
		t.Fatal("corpus fixture proto declares no message Item")
	}

	// The columns the corpus's two migrations actually declare, parsed
	// out of the SQL rather than substring-matched against it: a bare
	// strings.Contains(sql, "f_bytes") is true for a column, a comment,
	// and a CHECK constraint alike.
	columns := vocabularyMigrationColumns(itemsTableMigration, itemsVocabularyMigration)
	if len(columns) == 0 {
		t.Fatal("parsed ZERO columns out of the corpus migrations — this guard would assert nothing")
	}

	scalars := map[string]bool{}
	for _, k := range codegen.ProtoScalarKinds() {
		scalars[k] = true
	}

	var orphans []string
	for _, f := range msg.Fields {
		if !scalars[f.Kind] {
			continue // enums/messages/timestamps have their own coverage
		}
		if !columns[f.Name] {
			orphans = append(orphans, fmt.Sprintf("%s (%s)", f.Name, f.Kind))
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Errorf("corpus entity declares scalar fields with NO column in the applied schema: %s\n\n"+
			"A wire field with no column is not converted — the field travels no further "+
			"than the proto, so it is covered in appearance only. Add the column to "+
			"itemsVocabularyMigration in internal/cli/scaffold_frontend_e2e_test.go.\n"+
			"Parsed columns: %v",
			strings.Join(orphans, ", "), sortedVocabularyColumns(columns))
	}
}

// vocabularyMigrationColumns extracts declared column names from the
// corpus's CREATE TABLE / ALTER TABLE fixtures. It is deliberately small
// and literal-minded: the corpus migrations are fixtures this file's own
// tests own, one column per line, so a line-oriented reader is honest
// about what it can parse. Anything it cannot read is simply not
// reported as a column, which makes the test above stricter, never
// looser.
func vocabularyMigrationColumns(migrations ...string) map[string]bool {
	out := map[string]bool{}
	for _, sql := range migrations {
		for _, line := range strings.Split(sql, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "ADD COLUMN ")
			if line == "" || strings.HasPrefix(line, "--") ||
				strings.HasPrefix(line, "CREATE ") || strings.HasPrefix(line, "ALTER ") ||
				strings.HasPrefix(line, "CHECK") || strings.HasPrefix(line, ")") {
				continue
			}
			name, _, found := strings.Cut(line, " ")
			if !found {
				continue
			}
			if name = strings.TrimSpace(name); name != "" {
				out[name] = true
			}
		}
	}
	return out
}

func sortedVocabularyColumns(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
