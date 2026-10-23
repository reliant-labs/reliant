package cli

// `forge project shapes` must index the GENERATED store interfaces.
//
// WHY. A measured run read forge's own generator source —
// internal/generator/plan_orm_store_gen.go, `git log` on it included — 11
// times, to work out what the generated `db.Store` offers. That is the
// worst available source for the answer twice over: it is the code that
// WRITES the file rather than the file, and it describes every project
// rather than this one's schema. The generated file is in the reader's own
// tree and is specific to their entities.
//
// The reason they went to the generator is that nothing pointed at the
// file. `store_gen.go` shipped the same day the run happened; `shapes` —
// the recon verb agents already run first — indexed rpcs, messages, enums,
// tables, handlers and hooks, and simply did not mention stores.
//
// Unlike a struct, an interface IS fully rendered by `go doc`, so the
// detail column can name a command that actually terminates.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStoreFixture writes a miniature store_gen.go shaped like the real
// generated file. It is written at test time so nothing here can be
// passing off a transcription as a scan.
func writeStoreFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dbDir := filepath.Join(root, "internal", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := `package db

import "context"

// CrewStore is the generated CRUD surface for Crew.
type CrewStore interface {
	CreateCrew(ctx context.Context, msg *Crew) error
	UpdateCrewMasked(ctx context.Context, msg *Crew, fields []string) error
}

// crewStore is the unexported adapter and must NOT be indexed.
type crewStore struct{ db orm.Context }

// Store is every entity's store in one interface.
type Store interface {
	Crews() CrewStore
}
`
	if err := os.WriteFile(filepath.Join(dbDir, "store_gen.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// A test file in the same package must not contribute shapes.
	testSrc := "package db\n\ntype FakeStore interface{ Noop() }\n"
	if err := os.WriteFile(filepath.Join(dbDir, "store_gen_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatalf("write test fixture: %v", err)
	}
	return root
}

// TestScanStores_IndexesGeneratedStoreInterfaces is the core guard: the
// per-entity store and the aggregate both appear, with the command that
// prints their method set.
func TestScanStores_IndexesGeneratedStoreInterfaces(t *testing.T) {
	t.Parallel()
	root := writeStoreFixture(t)

	got := scanStores(filepath.Join(root, "internal", "db"), "internal/db")

	byName := map[string]shape{}
	for _, s := range got {
		byName[s.Name] = s
	}
	for _, want := range []string{"CrewStore", "Store"} {
		s, ok := byName[want]
		if !ok {
			t.Fatalf("%s was not indexed; a reader has nothing pointing at store_gen.go: %+v", want, got)
		}
		if s.Kind != "store" {
			t.Errorf("%s indexed as kind %q, want \"store\"", want, s.Kind)
		}
		if s.Line == 0 {
			t.Errorf("%s has no line number, so the next step is a search rather than a read", want)
		}
		// The name alone leaves "and what are its methods" unanswered —
		// which is the question that sent 11 turns into forge's generator.
		if !strings.Contains(s.Detail, "go doc ./internal/db "+want) {
			t.Errorf("%s detail = %q; it must name the command that prints the method set", want, s.Detail)
		}
	}

	// Unexported adapters and test types are noise: an index that lists the
	// private passthrough struct next to the interface makes the reader
	// pick, and picking wrong sends them back into the source.
	for _, unwanted := range []string{"crewStore", "FakeStore"} {
		if _, found := byName[unwanted]; found {
			t.Errorf("%s was indexed; only the exported, non-test interfaces belong here", unwanted)
		}
	}
}

// TestCollectShapes_IncludesStores pins that the scan is actually WIRED
// into the verb. A scanner nobody calls is the situation this whole
// change exists to fix, one level down.
func TestCollectShapes_IncludesStores(t *testing.T) {
	t.Parallel()
	root := writeStoreFixture(t)

	for _, s := range collectShapes(root) {
		if s.Kind == "store" && s.Name == "CrewStore" {
			return
		}
	}
	t.Error("collectShapes does not include stores, so `forge project shapes` still omits them")
}

// TestKindRank_StoresSortWithTheDataLayer keeps stores adjacent to the
// tables they persist, so a reader scanning the data half of the output
// sees the schema and the interface over it together rather than finding
// the interface parked after the frontend hooks.
func TestKindRank_StoresSortWithTheDataLayer(t *testing.T) {
	t.Parallel()
	if kindRank("store") <= kindRank("table") {
		t.Error("stores must sort after the tables they persist")
	}
	if kindRank("store") >= kindRank("hook") {
		t.Error("stores are backend shapes and must sort before frontend hooks")
	}
}
