// Tests for the pre/post rename-detection passes.
//
// FRICTION 2026-06-02: cp-forge `forgedb.Migrations()` →
// `forgedb.MigrationsFS` rename left internal/db/migrations.go
// orphaned. These tests pin the fix: a stale `pkgName.Name` reference
// in a hand-written file produces a warning identifying the dropped
// symbol, the call site, and the package.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// TestStepSnapshotTier1Exports_RecordsGoExports verifies the
// pre-codegen snapshot pass picks up the public identifiers of every
// certified Tier-1 Go file and skips Tier-2 / non-Go / disowned
// entries. Tier-1 status is read from the files themselves now: a
// stamped forge:hash marker IS the ownership record.
func TestStepSnapshotTier1Exports_RecordsGoExports(t *testing.T) {
	dir := t.TempDir()

	// db/embed.go — Tier-1 Go file with two public exports.
	writeStampedUnderDir(t, dir, "db/embed_gen.go", `package forgedb

var Migrations = "v1"

func Hello() {}
`)
	// hooks.ts — Tier-1 non-Go: skipped by the snapshotter.
	writeStampedUnderDir(t, dir, "web/hooks.ts", `export const Foo = 1;`)
	// disown.go — certified but disowned: skipped (forge no longer owns
	// it; the legacy fork state converts to disowned at migration time).
	writeStampedUnderDir(t, dir, "disown/disown.go", `package disown

var DisownedExport = "x"
`)
	// scaffolded.go — Tier-2 (no marker): skipped (rename detection is
	// Tier-1 only).
	writeUnderDir(t, dir, "scaffold/scaffolded.go", `package scaffold

var ScaffoldExport = "x"
`)

	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		"disown/disown.go": {Reason: "test", DisownedAt: "2026-06-01T00:00:00Z"},
	}}
	ctx := &pipelineContext{
		ProjectDir: dir,
		AbsPath:    dir,
		Checksums:  cs,
	}
	if err := stepSnapshotTier1Exports(ctx); err != nil {
		t.Fatalf("stepSnapshotTier1Exports: %v", err)
	}
	if got, ok := ctx.PriorExports["db/embed_gen.go"]; !ok {
		t.Errorf("PriorExports missing db/embed.go entry")
	} else {
		if got.PkgName != "forgedb" {
			t.Errorf("db/embed.go pkg = %q, want forgedb", got.PkgName)
		}
		if !sliceContains(got.Names, "Migrations") || !sliceContains(got.Names, "Hello") {
			t.Errorf("db/embed.go exports = %v, want both Migrations + Hello", got.Names)
		}
	}
	if _, ok := ctx.PriorExports["web/hooks.ts"]; ok {
		t.Errorf("PriorExports should skip non-Go Tier-1 files")
	}
	if _, ok := ctx.PriorExports["disown/disown.go"]; ok {
		t.Errorf("PriorExports should skip disowned files")
	}
	if _, ok := ctx.PriorExports["scaffold/scaffolded.go"]; ok {
		t.Errorf("PriorExports should skip Tier-2 files")
	}
}

// TestStepDetectRenamedExports_FlagsStaleCaller is the headline
// FRICTION reproduction: snapshot a `forgedb.Migrations` accessor,
// rewrite the file to use `MigrationsFS`, and verify the post-pass
// emits a warning naming the stale caller.
func TestStepDetectRenamedExports_FlagsStaleCaller(t *testing.T) {
	dir := t.TempDir()

	// 1. Hand-written caller — references the old name.
	writeUnderDir(t, dir, "internal/db/migrations.go", `package db

import "example.com/m/db"

func Apply() { _ = db.Migrations() }
`)

	// 2. Generated file (BEFORE the rename) — declares Migrations.
	// Stamped: the marker is what makes it a Tier-1 snapshot subject.
	writeStampedUnderDir(t, dir, "db/embed_gen.go", `package db

func Migrations() string { return "old" }
`)

	cs := &checksums.FileChecksums{}
	ctx := &pipelineContext{
		ProjectDir: dir,
		AbsPath:    dir,
		Checksums:  cs,
	}
	if err := stepSnapshotTier1Exports(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// 3. Simulate codegen rewriting db/embed.go with the new symbol.
	writeUnderDir(t, dir, "db/embed_gen.go", `package db

var MigrationsFS = "new"
`)

	// 4. Detection: capture stderr to verify the warning surfaces.
	stderr, restore := captureStderr(t)
	if err := stepDetectRenamedExports(ctx); err != nil {
		restore()
		t.Fatalf("detect: %v", err)
	}
	restore()
	out := stderr.String()
	if !strings.Contains(out, "Tier-1 rename detection") {
		t.Errorf("missing rename-detection banner; got:\n%s", out)
	}
	if !strings.Contains(out, "db/embed_gen.go") {
		t.Errorf("warning should name the renamed file; got:\n%s", out)
	}
	if !strings.Contains(out, "Migrations") {
		t.Errorf("warning should name the dropped symbol; got:\n%s", out)
	}
	if !strings.Contains(out, "internal/db/migrations.go") {
		t.Errorf("warning should name the stale caller; got:\n%s", out)
	}
}

// TestStepDetectRenamedExports_NoFalsePositive verifies the prefix-
// guard: `pkg.Migrations` must NOT match against `pkg.MigrationsFS`.
// Without isIdentChar(), the textual scan would over-flag MigrationsFS
// when scanning for a deleted "Migrations".
func TestStepDetectRenamedExports_NoFalsePositive(t *testing.T) {
	dir := t.TempDir()

	// Caller only references the NEW name, not the dropped one.
	writeUnderDir(t, dir, "callers/x.go", `package callers

import "example.com/m/db"

func Use() { _ = db.MigrationsFS }
`)
	// Old: declared both.
	writeStampedUnderDir(t, dir, "db/embed_gen.go", `package db

var Migrations = "old"
var MigrationsFS = "x"
`)
	cs := &checksums.FileChecksums{}
	ctx := &pipelineContext{ProjectDir: dir, AbsPath: dir, Checksums: cs}
	if err := stepSnapshotTier1Exports(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Drop Migrations from the new render — but the only call site
	// references the still-present MigrationsFS, NOT Migrations.
	writeUnderDir(t, dir, "db/embed_gen.go", `package db

var MigrationsFS = "x"
`)
	stderr, restore := captureStderr(t)
	if err := stepDetectRenamedExports(ctx); err != nil {
		restore()
		t.Fatalf("detect: %v", err)
	}
	restore()
	out := stderr.String()
	if strings.Contains(out, "Tier-1 rename detection") {
		t.Errorf("got false-positive rename warning when only the new symbol was referenced:\n%s", out)
	}
}

// TestStepDetectRenamedExports_SkipsGenFiles verifies the scanner
// skips _gen.go files. Forge owns those files; their stale references
// will be cleaned up the moment the codegen rewrites them.
func TestStepDetectRenamedExports_SkipsGenFiles(t *testing.T) {
	dir := t.TempDir()
	// The stale reference is in a generated file — should be ignored.
	writeUnderDir(t, dir, "handlers/billing/handlers_crud_gen.go", `package billing

import "example.com/m/db"

func init() { _ = db.Migrations() }
`)
	writeStampedUnderDir(t, dir, "db/embed_gen.go", `package db

func Migrations() string { return "old" }
`)
	cs := &checksums.FileChecksums{}
	ctx := &pipelineContext{ProjectDir: dir, AbsPath: dir, Checksums: cs}
	if err := stepSnapshotTier1Exports(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	writeUnderDir(t, dir, "db/embed_gen.go", `package db

var MigrationsFS = "new"
`)
	stderr, restore := captureStderr(t)
	if err := stepDetectRenamedExports(ctx); err != nil {
		restore()
		t.Fatalf("detect: %v", err)
	}
	restore()
	if strings.Contains(stderr.String(), "Tier-1 rename detection") {
		t.Errorf("scanner should skip _gen.go files; got false-positive warning:\n%s", stderr.String())
	}
}

// TestStepDetectRenamedExports_CrossPackageMoveSurfaces verifies the
// rename detector follows a symbol from its original package to a
// different package between renders. `forgedb.MigrationsFS` moves from
// `db/embed.go` (package forgedb) to `pkg/embed/embed.go` (package
// embed); a caller still referencing `forgedb.MigrationsFS` is stale
// and the warning should name the new location.
func TestStepDetectRenamedExports_CrossPackageMoveSurfaces(t *testing.T) {
	dir := t.TempDir()

	// Stale caller — references the OLD package name.
	writeUnderDir(t, dir, "internal/db/migrations.go", `package callers

import "example.com/m/db"

func Apply() { _ = forgedb.MigrationsFS }
`)

	// 1. Pre-rename: db/embed.go declares MigrationsFS in package forgedb.
	writeStampedUnderDir(t, dir, "db/embed_gen.go", `package forgedb

var MigrationsFS = "v1"
`)

	cs := &checksums.FileChecksums{}
	ctx := &pipelineContext{ProjectDir: dir, AbsPath: dir, Checksums: cs}
	if err := stepSnapshotTier1Exports(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// 2. Post-rename: db/embed.go no longer declares MigrationsFS.
	writeUnderDir(t, dir, "db/embed_gen.go", `package forgedb

var Other = "v2"
`)
	// And MigrationsFS now lives in a different package.
	writeUnderDir(t, dir, "pkg/embed/embed.go", `package embed

var MigrationsFS = "v2"
`)

	stderr, restore := captureStderr(t)
	if err := stepDetectRenamedExports(ctx); err != nil {
		restore()
		t.Fatalf("detect: %v", err)
	}
	restore()
	out := stderr.String()
	if !strings.Contains(out, "Tier-1 rename detection") {
		t.Errorf("missing rename-detection banner; got:\n%s", out)
	}
	if !strings.Contains(out, "MigrationsFS") {
		t.Errorf("warning should name the dropped symbol; got:\n%s", out)
	}
	if !strings.Contains(out, "moved: MigrationsFS now in package embed") {
		t.Errorf("warning should surface the cross-package move; got:\n%s", out)
	}
	if !strings.Contains(out, "pkg/embed/embed.go") {
		t.Errorf("warning should name the new declaration path; got:\n%s", out)
	}
	// And the stale `forgedb.MigrationsFS` caller is still flagged.
	if !strings.Contains(out, "internal/db/migrations.go") {
		t.Errorf("warning should name the stale caller; got:\n%s", out)
	}
}

// TestStepDetectRenamedExports_CollisionDisambiguates pins the
// disambiguation branch: when the dropped symbol is now declared in
// >1 package, the warning lists every location so the user picks the
// right destination.
func TestStepDetectRenamedExports_CollisionDisambiguates(t *testing.T) {
	dir := t.TempDir()

	// Pre-rename declarer.
	writeStampedUnderDir(t, dir, "db/embed_gen.go", `package forgedb

var Migrations = "v1"
`)
	// Stale caller of the old location.
	writeUnderDir(t, dir, "internal/use.go", `package use

import "example.com/m/db"

func Run() { _ = db.Migrations }
`)
	cs := &checksums.FileChecksums{}
	ctx := &pipelineContext{ProjectDir: dir, AbsPath: dir, Checksums: cs}
	if err := stepSnapshotTier1Exports(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Post: dropped from db/embed.go; declared in TWO other packages.
	writeUnderDir(t, dir, "db/embed_gen.go", `package forgedb

var Renamed = "v2"
`)
	writeUnderDir(t, dir, "pkg/embed/embed.go", `package embed

var Migrations = "v2"
`)
	writeUnderDir(t, dir, "pkg/migrations/migrations.go", `package migrations

var Migrations = "v2"
`)

	stderr, restore := captureStderr(t)
	if err := stepDetectRenamedExports(ctx); err != nil {
		restore()
		t.Fatalf("detect: %v", err)
	}
	restore()
	out := stderr.String()
	if !strings.Contains(out, "now declared in 2 packages") {
		t.Errorf("warning should announce the collision; got:\n%s", out)
	}
	if !strings.Contains(out, "embed (pkg/embed/embed.go)") {
		t.Errorf("warning should name embed package; got:\n%s", out)
	}
	if !strings.Contains(out, "migrations (pkg/migrations/migrations.go)") {
		t.Errorf("warning should name migrations package; got:\n%s", out)
	}
}

// writeUnderDir writes content under dir/rel, creating parent
// directories. Test helper — fails the test on I/O errors. Local to
// this file to avoid colliding with mustWrite in generate_cleanup_test.go
// (different signature).
func writeUnderDir(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// writeStampedUnderDir writes content under dir/rel with the embedded
// forge:hash marker stamped in — the self-certifying equivalent of
// "forge wrote this Tier-1 file on a previous run" (the manifest era
// recorded a hash in .forge/checksums.json instead).
func writeStampedUnderDir(t *testing.T, dir, rel, content string) {
	t.Helper()
	stamped, ok := checksums.Stamp(rel, []byte(content))
	if !ok {
		t.Fatalf("stamp %s: format is unstampable", rel)
	}
	writeUnderDir(t, dir, rel, string(stamped))
}

// sliceContains is the tiny string-set predicate used by the snapshot
// test. Local to this file to avoid colliding with the `contains`
// substring predicate in generate_test.go.
func sliceContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// captureStderr is defined in test_helpers.go (same package). It
// redirects os.Stderr to a pipe and returns a Builder populated when
// the returned restore() is called.
