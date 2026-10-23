// Tests for the ownership Inspector.
//
// In the self-certifying era ownership is read from the files
// themselves (the forge:hash marker scan) plus the two small state
// files — there is no manifest. Tier()/IsTier2() are gone with the
// manifest's tier field: Tier-2 files carry no record at all (user-
// owned by convention), so "tracked" now means marker / scoped-fallback
// entry / disown record. Keep the cases small and focused — the
// inspector is the single source of truth for ownership queries, so a
// regression here cascades into every downstream check.
package checksums

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// mustStamp writes Stamp(rel, content) under dir/rel.
func mustStamp(t *testing.T, dir, rel, content string) {
	t.Helper()
	stamped, ok := Stamp(rel, []byte(content))
	if !ok {
		t.Fatalf("Stamp(%q): unstampable", rel)
	}
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, stamped, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspector_OwnershipClassification(t *testing.T) {
	dir := t.TempDir()

	// Marker-bearing forge renders (Tier-1).
	mustStamp(t, dir, "pkg/app/bootstrap.go", "package app\n")
	mustStamp(t, dir, "web/hooks.ts", "export {}\n")
	// A stamped file the user edited afterwards: marker fails, but the
	// path is still tracked/Tier-1 (forge claims it; the drift guard
	// adjudicates the edit).
	mustStamp(t, dir, "pkg/app/edited_gen.go", "package app // v1\n")
	edited, _ := os.ReadFile(filepath.Join(dir, "pkg/app/edited_gen.go"))
	if err := os.WriteFile(filepath.Join(dir, "pkg/app/edited_gen.go"), append(edited, []byte("// edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plain user files: no marker, no record — untracked (the legacy
	// manifest's Tier-2 entries kept no record in the new world).
	mustWrite(t, dir, "pkg/app/app_extras.go", "package app\n")
	// Disowned: record in .forge/disowned.json, marker stripped.
	mustWrite(t, dir, "internal/svc/contract.go", "package svc\n")

	cs := &FileChecksums{
		Disowned:    map[string]DisownedEntry{"internal/svc/contract.go": {Reason: "ours"}},
		Unstampable: map[string]string{"config/app.json": BodyHash([]byte("{}\n"))},
	}
	insp := NewInspector(dir, cs)

	cases := []struct {
		path         string
		wantTracked  bool
		wantTier1    bool
		wantDisowned bool
		wantGo       bool
	}{
		{"pkg/app/bootstrap.go", true, true, false, true},
		{"pkg/app/edited_gen.go", true, true, false, true},
		{"pkg/app/app_extras.go", false, false, false, true},  // scaffold-once: no record
		{"internal/svc/contract.go", true, false, true, true}, // disowned: tracked, not Tier-1
		{"web/hooks.ts", true, true, false, false},
		{"config/app.json", true, true, false, false}, // scoped fallback entry
		{"never/seen.go", false, false, false, true},  // untracked
	}
	for _, tc := range cases {
		if got := insp.IsTracked(tc.path); got != tc.wantTracked {
			t.Errorf("IsTracked(%q) = %v, want %v", tc.path, got, tc.wantTracked)
		}
		if got := insp.IsTier1(tc.path); got != tc.wantTier1 {
			t.Errorf("IsTier1(%q) = %v, want %v", tc.path, got, tc.wantTier1)
		}
		if got := insp.IsDisowned(tc.path); got != tc.wantDisowned {
			t.Errorf("IsDisowned(%q) = %v, want %v", tc.path, got, tc.wantDisowned)
		}
		if got := insp.IsGo(tc.path); got != tc.wantGo {
			t.Errorf("IsGo(%q) = %v, want %v", tc.path, got, tc.wantGo)
		}
	}
}

func TestInspector_NilManifestSafe(t *testing.T) {
	// Construction with nil ownership state must produce a non-nil
	// Inspector whose queries return safe zero values. This is the
	// "fresh project with no .forge state files yet" code path — callers
	// should not have to nil-check before each query.
	insp := NewInspector(t.TempDir(), nil)
	if insp.IsTracked("anything.go") {
		t.Errorf("IsTracked on nil state should be false")
	}
	if insp.IsDisowned("anything.go") {
		t.Errorf("IsDisowned on nil state should be false")
	}
	if insp.IsTier1("anything.go") {
		t.Errorf("IsTier1 on nil state should be false")
	}
	if got := insp.Tier1GoFiles(); got != nil {
		t.Errorf("Tier1GoFiles on nil state = %v, want nil", got)
	}
	if got := insp.DisownedGoFilesByDir(); len(got) != 0 {
		t.Errorf("DisownedGoFilesByDir on nil state should be empty, got %v", got)
	}
}

func TestInspector_DisownedGoFilesByDir(t *testing.T) {
	cs := &FileChecksums{Disowned: map[string]DisownedEntry{
		"pkg/app/bootstrap.go": {Reason: "user"},
		"pkg/app/wire_gen.go":  {Reason: "migrated from legacy fork-era entry"},
		"internal/x/x.go":      {Reason: "user"},
		// Non-Go disowned entries must NOT appear in the Go grouping —
		// a YAML or TSX file cannot satisfy a Go package-local
		// reference, so the dangling-ref check ignores them.
		".github/workflows/ci.yml": {Reason: "user"},
		"web/hooks.ts":             {Reason: "user"},
	}}
	insp := NewInspector("", cs)
	got := insp.DisownedGoFilesByDir()
	want := map[string][]string{
		"pkg/app":    {"pkg/app/bootstrap.go", "pkg/app/wire_gen.go"},
		"internal/x": {"internal/x/x.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DisownedGoFilesByDir mismatch.\ngot:  %v\nwant: %v", got, want)
	}

	// Second call should return the cached map.
	if got2 := insp.DisownedGoFilesByDir(); !reflect.DeepEqual(got, got2) {
		t.Errorf("second call returned a different map; got %v", got2)
	}
}

func TestInspector_Tier1GoFiles(t *testing.T) {
	dir := t.TempDir()
	mustStamp(t, dir, "db/embed_gen.go", "package db\n")
	mustStamp(t, dir, "pkg/app/bootstrap.go", "package app\n")
	mustStamp(t, dir, "pkg/app/disowned.go", "package app\n") // marker lingers, but disowned
	mustStamp(t, dir, "web/hooks.ts", "export {}\n")          // skip non-Go
	mustWrite(t, dir, "scaffold/svc.go", "package svc\n")     // no marker: user-owned scaffold

	cs := &FileChecksums{Disowned: map[string]DisownedEntry{
		"pkg/app/disowned.go": {Reason: "user"},
	}}
	insp := NewInspector(dir, cs)
	got := insp.Tier1GoFiles()
	want := []string{"db/embed_gen.go", "pkg/app/bootstrap.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tier1GoFiles = %v, want %v", got, want)
	}
}

func TestInspector_GoSiblingsIn(t *testing.T) {
	dir := t.TempDir()
	// Materialize a package directory containing a mix of files.
	mustWrite(t, dir, "pkg/app/a.go", "package app\n")
	mustWrite(t, dir, "pkg/app/b.go", "package app\n")
	mustWrite(t, dir, "pkg/app/a_test.go", "package app\n") // excluded
	mustWrite(t, dir, "pkg/app/README.md", "doc\n")         // excluded
	mustWrite(t, dir, "pkg/app/sub/c.go", "package sub\n")  // excluded (subdir)

	insp := NewInspector(dir, &FileChecksums{})
	got, err := insp.GoSiblingsIn("pkg/app")
	if err != nil {
		t.Fatalf("GoSiblingsIn: %v", err)
	}
	want := []string{"pkg/app/a.go", "pkg/app/b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GoSiblingsIn = %v, want %v", got, want)
	}

	// Cached second read returns the same slice.
	got2, err := insp.GoSiblingsIn("pkg/app")
	if err != nil {
		t.Fatalf("second GoSiblingsIn: %v", err)
	}
	if !reflect.DeepEqual(got, got2) {
		t.Errorf("cached read mismatch: %v vs %v", got, got2)
	}

	// Missing directory returns an error.
	if _, err := insp.GoSiblingsIn("nope"); err == nil {
		t.Errorf("expected error for missing directory")
	}
}

func TestInspector_DeclaredTypesIn(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "pkg/app/types.go", `package app

type Workers struct{}
type Container struct{}
type internalAlias = int
`)
	mustWrite(t, dir, "pkg/app/broken.go", `package app

this is not valid go
`)
	mustWrite(t, dir, "pkg/app/funcs.go", `package app

// Only func declarations — no type decls.
func Run() {}
`)

	insp := NewInspector(dir, &FileChecksums{})

	got := insp.DeclaredTypesIn("pkg/app/types.go")
	want := map[string]bool{"Workers": true, "Container": true, "internalAlias": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeclaredTypesIn(types.go) = %v, want %v", got, want)
	}

	// Unparseable file → empty non-nil set.
	got = insp.DeclaredTypesIn("pkg/app/broken.go")
	if got == nil {
		t.Errorf("DeclaredTypesIn on broken file should return non-nil empty set")
	}
	if len(got) != 0 {
		t.Errorf("DeclaredTypesIn(broken.go) = %v, want empty", got)
	}

	// File with no type decls → empty non-nil set.
	got = insp.DeclaredTypesIn("pkg/app/funcs.go")
	if len(got) != 0 {
		t.Errorf("DeclaredTypesIn(funcs.go) = %v, want empty", got)
	}

	// Missing file → empty non-nil set, no error path (callers don't
	// need to distinguish "missing" from "no types declared" for the
	// ownership-resolution scan).
	got = insp.DeclaredTypesIn("pkg/app/nope.go")
	if got == nil || len(got) != 0 {
		t.Errorf("DeclaredTypesIn on missing file = %v, want non-nil empty set", got)
	}
}

func TestInspector_DeclaredTypesIn_CachesResult(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "x.go", `package x
type Foo struct{}
`)
	insp := NewInspector(dir, &FileChecksums{})
	first := insp.DeclaredTypesIn("x.go")

	// Overwrite the file with new content; the inspector must return
	// the cached parse so callers that batch queries inside one
	// pipeline run see a consistent view.
	mustWrite(t, dir, "x.go", `package x
type Bar struct{}
`)
	second := insp.DeclaredTypesIn("x.go")
	if !reflect.DeepEqual(first, second) {
		t.Errorf("expected cached result; first=%v second=%v", first, second)
	}
}

// mustWrite writes content under dir/rel, creating parent directories.
// Inlined here to keep the inspector tests self-contained — sibling
// test files in this package use their own helpers with different
// signatures.
func mustWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}
