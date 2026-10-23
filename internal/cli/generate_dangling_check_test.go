// Tests for the disowned-sibling dangling-reference check.
//
// FRICTION 2026-06-04: cp-forge layer-6 workers lane regenerates
// `pkg/app/app_gen.go` to reference a `Workers` type that lives in the
// forked-and-frozen `pkg/app/bootstrap.go`. These tests pin the check
// that converts that silent build break into a `forge generate`
// failure with concrete next-step instructions.
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// TestCheckDisownedDanglingRefs_TableDriven covers the cases laid out in
// the task brief, plus the package-local edge cases the implementation
// has to get right (predeclared types, qualified references, package
// directory isolation).
func TestCheckDisownedDanglingRefs_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		// files is the project tree to materialize: relPath -> contents.
		files map[string]string
		// disowned is the set of relPaths that should be marked
		// disowned (Tier-2 + marker) in the synthetic checksums manifest.
		disowned map[string]bool
		// tier2 is the set of relPaths to record at Tier-2 (so the
		// check skips them — they're scaffolds, not regenerated).
		tier2 map[string]bool
		// wantErrSubstrings, when non-empty, asserts the returned error
		// is non-nil and contains each substring. An empty slice
		// asserts the returned error is nil.
		wantErrSubstrings []string
	}{
		{
			name: "happy path: forked file defines type, non-forked file references it",
			// The classic clean case: bootstrap.go is forked and still
			// defines Workers; app_gen.go references it. No dangling.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type Workers struct{}
`,
				"pkg/app/app_gen.go": `package app
type App struct {
	Workers *Workers
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: nil,
		},
		{
			name: "dangling: non-forked file references type, forked file doesn't define it",
			// The headline FRICTION reproduction: bootstrap.go was
			// frozen pre-workers and never defined Workers.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
// frozen at pre-workers content; no Workers type
type App struct{}
`,
				"pkg/app/app_gen.go": `package app
type Container struct {
	Workers *Workers
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: []string{
				"Workers",
				"pkg/app/app_gen.go",
				"pkg/app/bootstrap.go",
				"disowned",
				"Re-adopt",
			},
		},
		{
			name: "type defined in the non-forked file itself: no error",
			// app_gen.go both references AND declares Workers — no
			// dangling, even though bootstrap.go is forked.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type App struct{}
`,
				"pkg/app/app_gen.go": `package app
type Workers struct{}
type Container struct {
	W *Workers
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: nil,
		},
		{
			name: "type defined in a non-forked sibling: no error",
			// extras_gen.go provides Workers and is NOT forked, so
			// forge will keep emitting it. No dangling.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type App struct{}
`,
				"pkg/app/extras_gen.go": `package app
type Workers struct{}
`,
				"pkg/app/app_gen.go": `package app
type Container struct {
	W *Workers
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: nil,
		},
		{
			name: "no disowned files: check is a no-op",
			// Without any disowned entries the check returns immediately
			// regardless of what references what.
			files: map[string]string{
				"pkg/app/app_gen.go": `package app
type Container struct {
	W *Workers
}
`,
			},
			disowned:          map[string]bool{},
			wantErrSubstrings: nil,
		},
		{
			name: "qualified reference is ignored",
			// `external.Workers` is a qualified reference; `go build`
			// would resolve it through imports. Out of scope here.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type App struct{}
`,
				"pkg/app/app_gen.go": `package app
import external "example.com/x"
type Container struct {
	W *external.Workers
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: nil,
		},
		{
			name: "predeclared types are not flagged",
			// `int`, `string`, `error`, etc. must not surface as
			// dangling; if they did, the check would fire on every
			// run for every package.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type App struct{}
`,
				"pkg/app/app_gen.go": `package app
type Container struct {
	Count    int
	Name     string
	Err      error
	Bytes    []byte
	Mapping  map[string]int
	Anything any
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: nil,
		},
		{
			name: "Tier-2 sibling is not scanned",
			// A Tier-2 (scaffolded, user-owned) file in the same
			// package may legitimately reference any name; it's not a
			// regenerated Tier-1 file so the check skips it.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type App struct{}
`,
				"pkg/app/app_extras.go": `package app
type Container struct {
	W *Workers
}
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			tier2: map[string]bool{
				"pkg/app/app_extras.go": true,
			},
			wantErrSubstrings: nil,
		},
		{
			name: "multiple dangling references group under one type",
			// Two regenerated files reference the same missing type —
			// the error groups them so the user sees one fix path
			// for both.
			files: map[string]string{
				"pkg/app/bootstrap.go": `package app
type App struct{}
`,
				"pkg/app/app_gen.go": `package app
type Container struct {
	W *Workers
}
`,
				"pkg/app/wire_extras_gen.go": `package app
func WireExtras() *Workers { return nil }
`,
			},
			disowned: map[string]bool{
				"pkg/app/bootstrap.go": true,
			},
			wantErrSubstrings: []string{
				"Workers",
				"pkg/app/app_gen.go",
				"pkg/app/wire_extras_gen.go",
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, contents := range tc.files {
				writeUnderDir(t, dir, rel, contents)
			}

			cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{}}
			// Tier-1 status is read from the files themselves now:
			// stamp every materialized file with its forge:hash marker
			// by default. Disowned files are recorded in cs.Disowned
			// and stay unmarked (disown strips the marker); Tier-2
			// files stay unmarked too (user-owned from birth).
			for rel := range tc.files {
				if tc.disowned[rel] {
					cs.Disowned[rel] = checksums.DisownedEntry{Reason: "test", DisownedAt: "2026-06-01T00:00:00Z"}
					continue
				}
				if tc.tier2[rel] {
					continue
				}
				stamped, ok := checksums.Stamp(rel, []byte(tc.files[rel]))
				if !ok {
					t.Fatalf("stamp %s: unstampable", rel)
				}
				writeUnderDir(t, dir, rel, string(stamped))
			}

			err := checkDisownedDanglingRefs(context.Background(), dir, cs)
			if len(tc.wantErrSubstrings) == 0 {
				if err != nil {
					t.Fatalf("expected no error; got:\n%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %v; got nil", tc.wantErrSubstrings)
			}
			msg := err.Error()
			for _, want := range tc.wantErrSubstrings {
				if !strings.Contains(msg, want) {
					t.Errorf("error message missing substring %q.\nFull error:\n%s", want, msg)
				}
			}
		})
	}
}

// TestCheckDisownedDanglingRefs_NilChecksums verifies the early-return
// path: a nil or empty checksum manifest must not panic and must
// report no error (a fresh project has nothing forked).
func TestCheckDisownedDanglingRefs_NilChecksums(t *testing.T) {
	dir := t.TempDir()
	if err := checkDisownedDanglingRefs(context.Background(), dir, nil); err != nil {
		t.Errorf("nil checksums: want nil error, got: %v", err)
	}
	empty := &checksums.FileChecksums{}
	if err := checkDisownedDanglingRefs(context.Background(), dir, empty); err != nil {
		t.Errorf("empty checksums: want nil error, got: %v", err)
	}
}

// TestCheckDisownedDanglingRefs_NonGoDisownedSkipped verifies the check
// ignores disowned entries that aren't Go files. A disowned YAML or
// .tsx file can't possibly affect the resolution of an unqualified Go
// type name, so it must not contribute to the per-package scan.
// (Legacy `forked: true` entries convert to disowned at migration time.)
func TestCheckDisownedDanglingRefs_NonGoDisownedSkipped(t *testing.T) {
	dir := t.TempDir()
	writeUnderDir(t, dir, ".github/workflows/ci.yml", "name: ci\n")
	appGen, ok := checksums.Stamp("pkg/app/app_gen.go", []byte(`package app
type Container struct {
	W *Workers
}
`))
	if !ok {
		t.Fatal("app_gen.go should be stampable")
	}
	writeUnderDir(t, dir, "pkg/app/app_gen.go", string(appGen))
	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		".github/workflows/ci.yml": {Reason: "test", DisownedAt: "2026-06-01T00:00:00Z"},
	}}
	if err := checkDisownedDanglingRefs(context.Background(), dir, cs); err != nil {
		t.Errorf("non-Go disowned entry should be skipped; got error:\n%v", err)
	}
}

// TestCheckDisownedDanglingRefs_PackageScopeIsolated verifies that a
// disowned file in one package does NOT cause unrelated references in
// other packages to be flagged. The package-local resolution rule
// means an unqualified `Workers` in `internal/foo/foo.go` cannot
// be satisfied by `pkg/app/bootstrap.go` — but it also cannot be
// dangling-due-to-the-disown either; that's a different package, the
// `Workers` there is presumably defined locally.
func TestCheckDisownedDanglingRefs_PackageScopeIsolated(t *testing.T) {
	dir := t.TempDir()
	writeUnderDir(t, dir, "pkg/app/bootstrap.go", `package app
type App struct{}
`)
	// internal/foo defines its own Workers locally; nothing dangling.
	fooGen, ok := checksums.Stamp("internal/foo/foo.go", []byte(`package foo
type Workers struct{}
type Container struct { W *Workers }
`))
	if !ok {
		t.Fatal("foo.go should be stampable")
	}
	writeUnderDir(t, dir, "internal/foo/foo.go", string(fooGen))
	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		"pkg/app/bootstrap.go": {Reason: "test", DisownedAt: "2026-06-01T00:00:00Z"},
	}}
	if err := checkDisownedDanglingRefs(context.Background(), dir, cs); err != nil {
		t.Errorf("cross-package reference should not be flagged; got:\n%v", err)
	}
}
