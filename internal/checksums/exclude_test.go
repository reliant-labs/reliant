package checksums

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func excludeProject(t *testing.T, globs ...string) string {
	t.Helper()
	root := t.TempDir()
	y := "name: x\nscaffold:\n  exclude:\n"
	for _, g := range globs {
		y += "    - " + g + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	ResetPerRunState()
	TakeExcludedSkips()
	return root
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExclude_Tier1WriteIsSkippedAndReported(t *testing.T) {
	root := excludeProject(t, "frontends/web/**")
	wrote, err := WriteGeneratedFile(root, "frontends/web/src/hooks/a-hooks.ts", []byte("export {}\n"), nil, true)
	if err != nil || wrote {
		t.Fatalf("excluded Tier-1 write: wrote=%v err=%v (want skipped, even with force)", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(root, "frontends/web/src/hooks/a-hooks.ts")); !os.IsNotExist(err) {
		t.Fatalf("excluded file exists (stat err %v)", err)
	}
	// Outside the glob, the write happens.
	if wrote, err := WriteGeneratedFile(root, "frontends/admin/src/a.ts", []byte("export {}\n"), nil, false); err != nil || !wrote {
		t.Fatalf("non-excluded write: wrote=%v err=%v", wrote, err)
	}
	skips := TakeExcludedSkips()
	if len(skips) != 1 || skips[0].Path != "frontends/web/src/hooks/a-hooks.ts" || skips[0].Glob != "frontends/web/**" {
		t.Fatalf("skips = %+v", skips)
	}
	if again := TakeExcludedSkips(); len(again) != 0 {
		t.Fatalf("skips are reported once: second take = %+v", again)
	}
}

func TestExclude_ExistingFileIsLeftExactlyAsItIs(t *testing.T) {
	root := excludeProject(t, ".github/workflows/deploy.yml")
	writeFile(t, root, ".github/workflows/deploy.yml", "# mine\n")
	if wrote, err := WriteGeneratedFile(root, ".github/workflows/deploy.yml", []byte("# forge's\n"), nil, true); err != nil || wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".github/workflows/deploy.yml")); string(b) != "# mine\n" {
		t.Fatalf("excluded file changed: %q", b)
	}
}

func TestExclude_ScaffoldIsNeverBornAndNotRecorded(t *testing.T) {
	root := excludeProject(t, "docs/")
	wrote, err := WriteScaffoldIfMissing(root, "docs/README.md", []byte("# hi\n"))
	if err != nil || wrote {
		t.Fatalf("excluded scaffold: wrote=%v err=%v", wrote, err)
	}
	if ScaffoldRecorded(root, "docs/README.md") {
		t.Fatal("an excluded scaffold got a birth record: lifting the glob later would read it as deleted")
	}
	if ScaffoldMayBeBorn(root, "docs/README.md") {
		t.Fatal("ScaffoldMayBeBorn says yes to an excluded path")
	}
	// An excluded file is never a pristine scaffold forge may refresh.
	writeFile(t, root, "docs/nav.tsx", "x")
	RecordScaffoldWithHash(root, "docs/nav.tsx", []byte("x"))
	if ScaffoldIsPristine(root, "docs/nav.tsx") {
		t.Fatal("excluded file reported pristine")
	}
}

func TestExclude_GeneratedSiblingConflictFails(t *testing.T) {
	root := excludeProject(t, "internal/app/mounts_services_gen.go")
	writeFile(t, root, "internal/app/compose_gen.go", "package app\n")
	_, err := WriteGeneratedFile(root, "internal/app/mounts_services_gen.go", []byte("package app\n"), nil, false)
	var conflict *ExcludeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *ExcludeConflictError", err)
	}
	if conflict.Dependent != "internal/app/compose_gen.go" || conflict.Glob != "internal/app/mounts_services_gen.go" {
		t.Fatalf("conflict = %+v", conflict)
	}
}

func TestExclude_AbsentGeneratedFileWithHandCodeConflicts(t *testing.T) {
	root := excludeProject(t, "internal/handlers/item/handlers_crud_ops_gen.go")
	writeFile(t, root, "internal/handlers/item/handlers_crud.go", "package item\n")
	_, err := WriteGeneratedFile(root, "internal/handlers/item/handlers_crud_ops_gen.go", []byte("package item\n"), nil, false)
	var conflict *ExcludeConflictError
	if !errors.As(err, &conflict) || conflict.Dependent != "internal/handlers/item/handlers_crud.go" {
		t.Fatalf("err = %v, want a conflict naming handlers_crud.go", err)
	}
	// Present, it is the project's to maintain — like a disowned file.
	writeFile(t, root, "internal/handlers/item/handlers_crud_ops_gen.go", "package item\n// mine\n")
	if wrote, err := WriteGeneratedFile(root, "internal/handlers/item/handlers_crud_ops_gen.go", []byte("package item\n"), nil, false); err != nil || wrote {
		t.Fatalf("present excluded file: wrote=%v err=%v", wrote, err)
	}
}

func TestExclude_WholePackageExcludedIsNoConflict(t *testing.T) {
	root := excludeProject(t, "internal/app/")
	writeFile(t, root, "internal/app/compose.go", "package app\n")
	if wrote, err := WriteGeneratedFile(root, "internal/app/mounts_services_gen.go", []byte("package app\n"), nil, false); err != nil || wrote {
		t.Fatalf("whole package excluded: wrote=%v err=%v", wrote, err)
	}
}

func TestExclude_GlobsFollowForgeYAMLEdits(t *testing.T) {
	root := excludeProject(t, "a/**")
	if _, ok := ExcludedBy(root, "a/x"); !ok {
		t.Fatal("a/x not excluded")
	}
	y := "name: x\n# a different size, so the cache sees the edit\n"
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ExcludedBy(root, "a/x"); ok {
		t.Fatal("exclusion survived its removal from forge.yaml")
	}
}
