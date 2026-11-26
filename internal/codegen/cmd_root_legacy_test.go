package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The retirement of cmd/<bin>/cmd/root_gen.go has to hold TWO shapes at once,
// and this file pins both.
//
// The hazard it guards is a real one that a first cut shipped: root.go and
// serve.go are scaffold-once, so a project born before the retirement keeps
// copies that call generatedCommands(), migrationSource(), migrationSourceDir
// and ServiceName. Deleting root_gen.go outright left every such project failing
// `go build` with five `undefined:` errors and no generate run able to recover —
// the files that needed rewriting were exactly the ones forge promised never to
// rewrite.

// writeLegacyRootGo lays down a cmd tree whose root.go is the PRE-retirement
// shape: the discriminator is the generatedCommands() call.
func writeLegacyRootGo(t *testing.T, dir, bin string) {
	t.Helper()
	treeDir := filepath.Join(dir, "cmd", bin, "cmd")
	if err := os.MkdirAll(treeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `package cmd

func newRootCmd(deps Deps) {
	for _, c := range generatedCommands(deps) {
		_ = c
	}
}
`
	if err := os.WriteFile(filepath.Join(treeDir, "root.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeModernRootGo lays down the CURRENT shape: ServiceName declared here, and
// commands arriving through the registry rather than generatedCommands().
func writeModernRootGo(t *testing.T, dir, bin string) {
	t.Helper()
	treeDir := filepath.Join(dir, "cmd", bin, "cmd")
	if err := os.MkdirAll(treeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `package cmd

const ServiceName = "demo"

var registeredCommands []Factory

func newRootCmd(deps Deps) {
	for _, f := range registeredCommands {
		_ = f
	}
}
`
	if err := os.WriteFile(filepath.Join(treeDir, "root.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDBCmdFile(t *testing.T, dir, bin string) {
	t.Helper()
	p := filepath.Join(dir, "cmd", bin, "cmd", "db.go")
	if err := os.WriteFile(p, []byte("package cmd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSyncLegacyRootGen_KeepsEmittingForAPreRetirementRootGo is the
// backwards-compatibility half: a project whose root.go still calls
// generatedCommands() must keep getting root_gen.go, with every symbol that
// root.go and serve.go reference.
func TestSyncLegacyRootGen_KeepsEmittingForAPreRetirementRootGo(t *testing.T) {
	dir := t.TempDir()
	writeLegacyRootGo(t, dir, "demo")
	writeDBCmdFile(t, dir, "demo")

	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatalf("SyncLegacyRootGen: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, legacyRootGenRelPath("demo")))
	if err != nil {
		t.Fatalf("legacy root_gen.go was not emitted for a root.go that needs it: %v", err)
	}
	src := string(body)

	// It must PARSE — an emitted file that does not compile is the failure
	// mode this whole path exists to prevent.
	if _, err := parser.ParseFile(token.NewFileSet(), "root_gen.go", src, parser.AllErrors); err != nil {
		t.Fatalf("emitted legacy root_gen.go is not valid Go: %v\n---\n%s", err, src)
	}

	// Every symbol the old scaffold-once files name.
	for _, want := range []string{
		`const ServiceName = "demo"`,
		"const migrationSourceDir =",
		"func migrationSource() fs.FS",
		"func generatedCommands(",
		"newDBCmd(deps)", // db.go exists, so the command is wired
	} {
		if !strings.Contains(src, want) {
			t.Errorf("legacy root_gen.go is missing %q — a pre-retirement root.go/serve.go "+
				"references it and the project will not build:\n%s", want, src)
		}
	}
}

// TestSyncLegacyRootGen_WithoutDBFileStillCompiles covers the project that has
// no db.go: generatedCommands must still EXIST (root.go calls it) but return
// nothing, and the file must not name a constructor that isn't there.
func TestSyncLegacyRootGen_WithoutDBFileStillCompiles(t *testing.T) {
	dir := t.TempDir()
	writeLegacyRootGo(t, dir, "demo") // no db.go

	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatalf("SyncLegacyRootGen: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, legacyRootGenRelPath("demo")))
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	if _, err := parser.ParseFile(token.NewFileSet(), "root_gen.go", src, parser.AllErrors); err != nil {
		t.Fatalf("emitted legacy root_gen.go is not valid Go: %v\n---\n%s", err, src)
	}
	if strings.Contains(src, "newDBCmd") {
		t.Errorf("no db.go was scaffolded, so naming newDBCmd would not compile:\n%s", src)
	}
	if !strings.Contains(src, "func generatedCommands(") {
		t.Errorf("root.go calls generatedCommands unconditionally; it must still be declared:\n%s", src)
	}
}

// TestSyncLegacyRootGen_MigrationSourceTracksTheSQLOnDisk pins the one fact in
// the legacy file that is not constant: a project that writes its first
// migration must get the embedded set, not a permanent nil.
func TestSyncLegacyRootGen_MigrationSourceTracksTheSQLOnDisk(t *testing.T) {
	dir := t.TempDir()
	writeLegacyRootGo(t, dir, "demo")

	// No SQL yet: db/embed_gen.go does not exist, so naming it would not build.
	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, legacyRootGenRelPath("demo")))
	if !strings.Contains(string(before), "return nil") {
		t.Errorf("with no migrations, migrationSource must return nil:\n%s", before)
	}
	if strings.Contains(string(before), "/db\"") {
		t.Errorf("with no migrations, the embedded db package must not be imported:\n%s", before)
	}

	// Write one, and the same call must flip to the embedded set.
	migDir := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "0001_init.up.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, legacyRootGenRelPath("demo")))
	if !strings.Contains(string(after), "forgedb.MigrationsFS") {
		t.Errorf("after writing a migration, migrationSource must return the embedded set:\n%s", after)
	}
}

// TestSyncLegacyRootGen_ReclaimsTheFileOnceRootGoIsMigrated is the other half,
// and the one that makes the compatibility file temporary rather than forever.
//
// It is also a correctness requirement, not just tidiness: the modern root.go
// declares ServiceName itself, so a leftover root_gen.go declaring it too is a
// redeclaration error in the same package.
func TestSyncLegacyRootGen_ReclaimsTheFileOnceRootGoIsMigrated(t *testing.T) {
	dir := t.TempDir()
	writeLegacyRootGo(t, dir, "demo")
	writeDBCmdFile(t, dir, "demo")

	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, legacyRootGenRelPath("demo"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: legacy file should exist first: %v", err)
	}

	// The user follows the migration note in the file's own header.
	writeModernRootGo(t, dir, "demo")
	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		body, _ := os.ReadFile(path)
		t.Errorf("root_gen.go survived a migrated root.go (stat err: %v). It declares "+
			"ServiceName, which the new root.go also declares, so the package no longer "+
			"compiles:\n%s", err, body)
	}
}

// TestSyncLegacyRootGen_NewProjectNeverGetsTheFile is the default path: a
// project scaffolded today has a modern root.go from birth and must never see
// this compatibility file at all.
func TestSyncLegacyRootGen_NewProjectNeverGetsTheFile(t *testing.T) {
	dir := t.TempDir()
	writeModernRootGo(t, dir, "demo")

	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, legacyRootGenRelPath("demo"))); !os.IsNotExist(err) {
		t.Errorf("a new-shape project must not get root_gen.go (stat err: %v)", err)
	}
}

// TestSyncLegacyRootGen_SuppliesRegisterCommandForALegacyTree covers the gap the
// two shapes leave between them.
//
// Files scaffolded AFTER the retirement self-register: cmd-tree-auth.go.tmpl and
// cmd-tree-db.go.tmpl both emit `func init() { registerCommand(newXCmd) }`. That
// helper is declared in the MODERN root.go — so a legacy project, whose
// scaffold-once root.go predates it and will never be rewritten, gets the new
// file and stops compiling with `undefined: registerCommand`.
//
// The registry has to come from the same compatibility file that already covers
// the rest of the pre-retirement surface, and generatedCommands must return what
// was registered, or a registered command exists but never reaches the tree.
func TestSyncLegacyRootGen_SuppliesRegisterCommandForALegacyTree(t *testing.T) {
	dir := t.TempDir()
	writeLegacyRootGo(t, dir, "demo")
	writeDBCmdFile(t, dir, "demo")

	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatalf("SyncLegacyRootGen: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, legacyRootGenRelPath("demo")))
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	if _, err := parser.ParseFile(token.NewFileSet(), "root_gen.go", src, parser.AllErrors); err != nil {
		t.Fatalf("emitted legacy root_gen.go is not valid Go: %v\n---\n%s", err, src)
	}

	if !strings.Contains(src, "func registerCommand(") {
		t.Errorf("a post-retirement scaffold-once file (auth.go, db.go) calls registerCommand "+
			"from an init(); a legacy root.go does not declare it, so the compatibility file "+
			"must:\n%s", src)
	}
	if !strings.Contains(src, "var registeredCommands") {
		t.Errorf("registerCommand needs the slice it appends to:\n%s", src)
	}
	// Registering into a slice nothing reads would compile and silently drop
	// the command, which is the harder bug to see.
	if !strings.Contains(src, "registeredCommands") ||
		!strings.Contains(src, "func generatedCommands(") {
		t.Fatalf("generatedCommands is what root.go ranges over; it must return the "+
			"registered commands or they never reach the tree:\n%s", src)
	}
	genBody := src[strings.Index(src, "func generatedCommands("):]
	if !strings.Contains(genBody, "registeredCommands") {
		t.Errorf("generatedCommands must include the registered commands, or a file that "+
			"self-registers compiles but contributes nothing:\n%s", genBody)
	}
}

// TestSyncLegacyRootGen_RegistryIsDeclaredOnlyOnce guards the mirror-image
// failure: the modern root.go declares registeredCommands and registerCommand
// itself, so emitting them here for a migrated project would be a
// redeclaration — the same package-level collision that made ServiceName worth
// reclaiming the file over.
func TestSyncLegacyRootGen_RegistryIsDeclaredOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	writeModernRootGo(t, dir, "demo")

	if err := SyncLegacyRootGen(dir, "example.com/demo", "demo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, legacyRootGenRelPath("demo"))); !os.IsNotExist(err) {
		t.Errorf("a modern root.go declares the registry itself; the compatibility file "+
			"must not also declare it (stat err: %v)", err)
	}
}
