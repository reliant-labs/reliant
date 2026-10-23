// File: internal/cli/scaffold/entity_test.go
//
// The proto-injection half of an entity birth: quintet completion
// (completeEntityCRUDProto) writing the CRUD RPCs and envelope messages into
// a service proto that already declares the entity message.
//
// The import assertions are the load-bearing ones. Emitting an rpc that
// carries `(forge.v1.method)` options, or a field carrying
// `(buf.validate.field)` options, WITHOUT the matching import fails the whole
// next `forge generate` inside buf with "unknown extension …" — the proto
// text reads as perfectly correct and nothing compiles. That is the repo's
// most expensive recurring bug shape, so each import forge can emit a use of
// has a test that it is also emitted.

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/migrationver"
)

const entityTestProto = `syntax = "proto3";

package services.item.v1;

import "forge/v1/forge.proto";

option go_package = "example.com/x/gen/services/item/v1;itemv1";

service ItemService {
  option (forge.v1.service) = {
    name: "ItemService"
  };

  rpc GetItem(GetItemRequest) returns (GetItemResponse) {}
}

message GetItemRequest {
  string id = 1;
}

message GetItemResponse {
  string id = 1;
}

// forge:entity
message Bookmark {
  string id = 1;
  string url = 2;
  repeated string tags = 3;
  bool done = 4;
}
`

func scaffoldEntityProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\nmodule: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	protoDir := filepath.Join(root, "proto", "services", "item", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "item.proto"), []byte(entityTestProto), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestWriteBirthMigration_TimestampVersionedAfterExistingAndWritesNoDown
// pins the versioning every birth shares — a timestamp, sorting after the
// existing sequential files it never renames — and the roll-forward policy:
// a birth writes the up migration and nothing that claims to reverse it.
func TestWriteBirthMigration_TimestampVersionedAfterExistingAndWritesNoDown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00007_other.up.sql"), []byte("CREATE TABLE x (id TEXT PRIMARY KEY);"), 0o644); err != nil {
		t.Fatal(err)
	}
	up, err := writeBirthMigration(dir, "things", "CREATE TABLE things (id TEXT PRIMARY KEY);\n")
	if err != nil {
		t.Fatal(err)
	}
	// The birth is timestamp-versioned and sorts after the existing
	// sequential file, which is what lets old migrations stay as they are.
	base := filepath.Base(up)
	version, stem, ok := strings.Cut(base, "_")
	if !ok || stem != "create_things.up.sql" {
		t.Fatalf("unexpected birth migration name %q", base)
	}
	if !migrationver.IsTimestamp(version) {
		t.Errorf("birth migration %q is not timestamp-versioned", base)
	}
	if version <= "00007" {
		t.Errorf("birth migration %q must sort after the existing 00007", base)
	}
	downs, _ := filepath.Glob(filepath.Join(dir, "*.down.sql"))
	if len(downs) != 0 {
		t.Errorf("a birth must write no down migration, found %v", downs)
	}
}

// completeQuintetIn runs the real quintet completion over the entity message
// declared in protoPath, feeding it the field list production feeds it: the
// message's own proto fields, read back by the raw scanner.
func completeQuintetIn(t *testing.T, root, protoPath, entity string) {
	t.Helper()
	scan, err := codegen.ScanRawProtoDir(filepath.Dir(protoPath))
	if err != nil {
		t.Fatalf("scan authored proto: %v", err)
	}
	m, ok := scan.MessageByName(entity)
	if !ok {
		t.Fatalf("raw scan did not find the %s entity message", entity)
	}
	fields, _ := entityFieldsFromSchemaDefs("services.item.v1", m.Fields)
	if _, err := completeEntityCRUDProto(root, protoPath, m.File, entity, fields, false); err != nil {
		t.Fatalf("complete CRUD quintet: %v", err)
	}
}

func TestCompleteEntityCRUDProto(t *testing.T) {
	root := scaffoldEntityProject(t)
	protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
	completeQuintetIn(t, root, protoPath, "Bookmark")
	got := readFileT(t, protoPath)

	for _, want := range []string{
		`import "google/protobuf/timestamp.proto";`,
		`import "google/protobuf/field_mask.proto";`,
		"rpc CreateBookmark(CreateBookmarkRequest) returns (CreateBookmarkResponse)",
		"rpc ListBookmarks(ListBookmarksRequest) returns (ListBookmarksResponse)",
		"message UpdateBookmarkRequest {",
		"google.protobuf.FieldMask update_mask = 2;",
		// The Create request FLATTENS the entity's client-settable fields,
		// renumbered contiguously (id is server-owned and omitted).
		"string url = 1;",
		"repeated string tags = 2;",
		"bool done = 3;",
		// List's filter surface is derived from the entity's plain scalars:
		// a text field yields `search`, each bool yields its own filter.
		"optional string search = 3;",
		"optional bool done = 4;",
		"repeated Bookmark bookmarks = 1;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("proto missing %q:\n%s", want, got)
		}
	}

	// The author's entity message is never rewritten — completion adds the
	// surface AROUND it.
	if n := strings.Count(got, "message Bookmark {"); n != 1 {
		t.Errorf("the entity message was re-emitted (%d copies):\n%s", n, got)
	}

	// RPCs must land INSIDE the service block.
	svcEnd := strings.Index(got, "\n}")
	if createIdx := strings.Index(got, "rpc CreateBookmark"); createIdx > svcEnd {
		t.Errorf("CreateBookmark rpc landed outside the service block")
	}

	// Idempotent: a second run adds nothing.
	completeQuintetIn(t, root, protoPath, "Bookmark")
	again := readFileT(t, protoPath)
	for _, once := range []string{"message CreateBookmarkRequest {", "rpc CreateBookmark", "message ListBookmarksResponse {"} {
		if n := strings.Count(again, once); n != 1 {
			t.Errorf("re-running completion duplicated %q (%d copies)", once, n)
		}
	}
}

// entityBareServiceProto is the shape `forge scaffold service` scaffolds:
// no RPCs yet, and therefore NO forge/v1/forge.proto import — plus the
// entity message an author has just written into it. Completion adds RPCs
// carrying (forge.v1.method) options, so it MUST also add the import or
// `buf` fails every subsequent generate with "unknown extension
// forge.v1.method" (journey fr-af7355dd63).
const entityBareServiceProto = `syntax = "proto3";

package services.item.v1;

option go_package = "example.com/x/gen/services/item/v1;itemv1";

// ItemService defines the item service RPCs.
service ItemService {
  // TODO: Add your RPC methods here.
}

// forge:entity
message Bookmark {
  string id = 1;
  string url = 2;
}
`

func TestCompleteEntityCRUDProto_AddsForgeImportToBareServiceProto(t *testing.T) {
	root := scaffoldEntityProject(t)
	protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
	if err := os.WriteFile(protoPath, []byte(entityBareServiceProto), 0o644); err != nil {
		t.Fatal(err)
	}
	completeQuintetIn(t, root, protoPath, "Bookmark")
	got := readFileT(t, protoPath)

	if !strings.Contains(got, `import "forge/v1/forge.proto";`) {
		t.Fatalf("emitted (forge.v1.method) options without importing forge/v1/forge.proto — buf fails with 'unknown extension forge.v1.method':\n%s", got)
	}
	// The import must land between the package line and the service block
	// (i.e. in the file header, not appended after messages).
	impIdx := strings.Index(got, `import "forge/v1/forge.proto";`)
	pkgIdx := strings.Index(got, "package services.item.v1;")
	svcIdx := strings.Index(got, "service ItemService {")
	if impIdx < pkgIdx || impIdx > svcIdx {
		t.Errorf("forge import landed outside the file header (pkg=%d import=%d service=%d):\n%s", pkgIdx, impIdx, svcIdx, got)
	}

	// Completing a SECOND entity in the same file must not duplicate the import.
	appended := got + "\n// forge:entity\nmessage Widget {\n  string id = 1;\n}\n"
	if err := os.WriteFile(protoPath, []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}
	completeQuintetIn(t, root, protoPath, "Widget")
	if again := readFileT(t, protoPath); strings.Count(again, `import "forge/v1/forge.proto";`) != 1 {
		t.Errorf("a second completion duplicated the forge import:\n%s", again)
	}
}

// TestCompleteEntityCRUDProto_UserReorderedImports pins that the ensure-
// import logic appends after the LAST import regardless of how the user
// ordered the block, and never duplicates an import that is already
// present anywhere in it.
func TestCompleteEntityCRUDProto_UserReorderedImports(t *testing.T) {
	root := scaffoldEntityProject(t)
	protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
	reordered := `syntax = "proto3";

package services.item.v1;

import "google/protobuf/timestamp.proto";
import "forge/v1/forge.proto";

option go_package = "example.com/x/gen/services/item/v1;itemv1";

service ItemService {
}

// forge:entity
message Bookmark {
  string id = 1;
  string url = 2;
}
`
	if err := os.WriteFile(protoPath, []byte(reordered), 0o644); err != nil {
		t.Fatal(err)
	}
	completeQuintetIn(t, root, protoPath, "Bookmark")
	got := readFileT(t, protoPath)
	for _, imp := range []string{
		`import "forge/v1/forge.proto";`,
		`import "google/protobuf/timestamp.proto";`,
		`import "google/protobuf/field_mask.proto";`,
	} {
		if n := strings.Count(got, imp); n != 1 {
			t.Errorf("%s appears %d times, want exactly 1:\n%s", imp, n, got)
		}
	}
	// New imports must still land in the header (after the existing
	// import block, before the service).
	fmIdx := strings.Index(got, `import "google/protobuf/field_mask.proto";`)
	svcIdx := strings.Index(got, "service ItemService {")
	if fmIdx > svcIdx {
		t.Errorf("field_mask import landed after the service block:\n%s", got)
	}
}

// completeAppendOnlyQuintetIn is completeQuintetIn with the
// `// forge:append-only` half of the contract armed: Update and Delete are
// filtered out, so the injected text is Create/Get/List only.
func completeAppendOnlyQuintetIn(t *testing.T, root, protoPath, entity string) {
	t.Helper()
	scan, err := codegen.ScanRawProtoDir(filepath.Dir(protoPath))
	if err != nil {
		t.Fatalf("scan authored proto: %v", err)
	}
	m, ok := scan.MessageByName(entity)
	if !ok {
		t.Fatalf("raw scan did not find the %s entity message", entity)
	}
	fields, _ := entityFieldsFromSchemaDefs("services.item.v1", m.Fields)
	if _, err := completeEntityCRUDProto(root, protoPath, m.File, entity, fields, true); err != nil {
		t.Fatalf("complete append-only quintet: %v", err)
	}
}

// TestCompleteEntityCRUDProto_AppendOnlyOmitsUnusedImports is the
// regression-lock for a service born failing `forge lint`.
//
// An append-only entity gets no Update rpc, so nothing types an
// `update_mask` and nothing names google.protobuf.FieldMask. The injector
// used to add field_mask.proto unconditionally anyway, and buf lint rejects
// an unused import — so a service whose entities are ALL append-only (a
// ledger, an audit log, a readings table: an ordinary thing to want) failed
// the gate forge itself tells you to run, before its author wrote a line.
//
// The assertion is the general one, not a field_mask special case: every
// import in the file must be named by something in it.
func TestCompleteEntityCRUDProto_AppendOnlyOmitsUnusedImports(t *testing.T) {
	root := scaffoldEntityProject(t)
	protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
	completeAppendOnlyQuintetIn(t, root, protoPath, "Bookmark")
	got := readFileT(t, protoPath)

	// The append-only contract itself still holds: three verbs, not five.
	for _, want := range []string{
		"rpc CreateBookmark(CreateBookmarkRequest) returns (CreateBookmarkResponse)",
		"rpc GetBookmark(GetBookmarkRequest) returns (GetBookmarkResponse)",
		"rpc ListBookmarks(ListBookmarksRequest) returns (ListBookmarksResponse)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("append-only quintet missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"rpc UpdateBookmark", "rpc DeleteBookmark"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("append-only entity got %q:\n%s", unwanted, got)
		}
	}

	// Nothing references FieldMask, so the import must not be there.
	if strings.Contains(got, "FieldMask") {
		t.Fatalf("append-only file unexpectedly names FieldMask:\n%s", got)
	}
	if strings.Contains(got, `import "google/protobuf/field_mask.proto";`) {
		t.Errorf("unused field_mask import — buf lint fails this file with "+
			`'Import "google/protobuf/field_mask.proto" is unused':`+"\n%s", got)
	}
	assertNoUnusedProtoImports(t, got)
}

// TestCompleteEntityCRUDProto_MutableKeepsFieldMask pins the other half: a
// normal entity DOES get Update, so field_mask must still be imported. The
// fix above must not trade one broken file for another.
func TestCompleteEntityCRUDProto_MutableKeepsFieldMask(t *testing.T) {
	root := scaffoldEntityProject(t)
	protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
	completeQuintetIn(t, root, protoPath, "Bookmark")
	got := readFileT(t, protoPath)

	if !strings.Contains(got, "google.protobuf.FieldMask update_mask = 2;") {
		t.Fatalf("mutable entity lost its update_mask:\n%s", got)
	}
	if !strings.Contains(got, `import "google/protobuf/field_mask.proto";`) {
		t.Errorf("update_mask emitted without its import — buf fails with " +
			"'unknown type google.protobuf.FieldMask'")
	}
	assertNoUnusedProtoImports(t, got)
}

// TestCompleteEntityCRUDProto_MixedServiceKeepsFieldMask covers the case the
// bug hid behind: a service holding one mutable entity and one append-only
// entity. The mutable one's Update supplies a real FieldMask use, so the
// import belongs — and completing the append-only entity afterwards must not
// strip an import the file still needs.
func TestCompleteEntityCRUDProto_MixedServiceKeepsFieldMask(t *testing.T) {
	root := scaffoldEntityProject(t)
	protoPath := filepath.Join(root, "proto", "services", "item", "v1", "item.proto")
	completeQuintetIn(t, root, protoPath, "Bookmark")

	appended := readFileT(t, protoPath) + "\n// forge:entity\n// forge:append-only\nmessage Reading {\n  string id = 1;\n  string source = 2;\n}\n"
	if err := os.WriteFile(protoPath, []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}
	completeAppendOnlyQuintetIn(t, root, protoPath, "Reading")
	got := readFileT(t, protoPath)

	if strings.Contains(got, "rpc UpdateReading") {
		t.Errorf("append-only Reading got an Update rpc:\n%s", got)
	}
	if n := strings.Count(got, `import "google/protobuf/field_mask.proto";`); n != 1 {
		t.Errorf("field_mask import appears %d times, want exactly 1 — Bookmark's "+
			"update_mask still needs it:\n%s", n, got)
	}
	assertNoUnusedProtoImports(t, got)
}

// assertNoUnusedProtoImports is the check buf lint performs, applied to the
// imports forge itself injects: every imported file must be named by
// something in the text. Pinning the rule rather than one import means a
// future verb filter cannot reintroduce this bug under a different symbol.
func assertNoUnusedProtoImports(t *testing.T, proto string) {
	t.Helper()
	// The symbol each injectable import exists to provide. An import forge
	// does not inject (the user's own, a split-proto entity file) is not
	// forge's to judge, so it is not listed.
	for _, imp := range []struct{ path, symbol string }{
		{"google/protobuf/field_mask.proto", "google.protobuf.FieldMask"},
		{"google/protobuf/timestamp.proto", "google.protobuf.Timestamp"},
		{"buf/validate/validate.proto", "buf.validate."},
		{"forge/v1/forge.proto", "forge.v1."},
	} {
		if !strings.Contains(proto, `import "`+imp.path+`";`) {
			continue
		}
		if !strings.Contains(proto, imp.symbol) {
			t.Errorf("import %q is unused (nothing names %s) — buf lint fails this file:\n%s",
				imp.path, imp.symbol, proto)
		}
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// birthMigrationPath finds the birth migration for stem under the project's
// db/migrations, failing the test if it is absent or ambiguous.
//
// Births are timestamp-versioned at the moment they are written, so a test
// cannot name the file it expects without asserting on the clock. The stem
// is the part forge actually derives (create_invoices from the Invoice
// message), and it is what these tests mean to pin.
func birthMigrationPath(t *testing.T, projectDir, stem string) string {
	t.Helper()
	pattern := filepath.Join(projectDir, "db", "migrations", "*_"+stem+".up.sql")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one migration matching %s, got %v", pattern, matches)
	}
	return matches[0]
}
