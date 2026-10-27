// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// multiRepo makes a two-checkout workspace the way B1 does.
func (f *fixture) multiRepo(id string) Worktree {
	f.t.Helper()
	ws := filepath.Join(f.root, "proj", id)
	for i, c := range []string{"api", "web"} {
		run(f.t, f.project, "worktree", "add", "-q", "-b", "feat/"+id+string(rune('a'+i)), filepath.Join(ws, c), "main")
		if err := LockCheckout(context.Background(), filepath.Join(ws, c), id); err != nil {
			f.t.Fatal(err)
		}
	}
	return Worktree{ID: id, Path: ws, State: StateArchived, BaseBranch: "main", Fence: "5"}
}

func TestRootFiles_SmallScratchIsSavedThenTheWorkspaceIsRemoved(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a two-checkout workspace through a full reclaim (~66 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.multiRepo("scratchy")
	write(t, filepath.Join(w.Path, "NOTES.md"), "plan\n")
	write(t, filepath.Join(w.Path, "scratch", "out", "log.txt"), "output\n")
	if err := os.Symlink("NOTES.md", filepath.Join(w.Path, "latest")); err != nil {
		t.Fatal(err)
	}

	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonOutsideFiles || !exists(filepath.Join(w.Path, "NOTES.md")) {
		t.Fatalf("automatic cleanup must preserve loose root files in place: %+v", got)
	}
	// Explicit user-confirmed recovery retains the established save/verify path.
	got = f.confirmed(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("confirmed cleanup: %+v", got)
	}
	want := filepath.Join(filepath.Dir(f.root), "reclaimed", "scratchy")
	if got.ReclaimedTo != want {
		t.Fatalf("ReclaimedTo = %q, want %q", got.ReclaimedTo, want)
	}
	for rel, body := range map[string]string{"NOTES.md": "plan\n", "scratch/out/log.txt": "output\n"} {
		b, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(rel)))
		if err != nil || string(b) != body {
			t.Errorf("saved %s = %q, %v; want %q", rel, b, err, body)
		}
	}
	if target, err := os.Readlink(filepath.Join(want, "latest")); err != nil || target != "NOTES.md" {
		t.Errorf("saved symlink = %q, %v", target, err)
	}
}

func TestRootFiles_ConfirmedPathSavesThemToo(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a two-checkout workspace through the confirmed path (~59 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.multiRepo("confirmed")
	write(t, filepath.Join(w.Path, "NOTES.md"), "plan\n")
	write(t, filepath.Join(w.Path, "api", "wip.txt"), "unsaved work in a checkout\n")
	got := f.confirmed(w)
	if got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("got %+v exists=%v, want removed", got, exists(w.Path))
	}
	if b, err := os.ReadFile(filepath.Join(got.ReclaimedTo, "NOTES.md")); err != nil || string(b) != "plan\n" {
		t.Fatalf("saved NOTES.md = %q, %v", b, err)
	}
}

func TestRootFiles_PastTheCapHoldsAndSavesNothing(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	f.idle()
	f.r.RootFilesCap = 8
	w := f.multiRepo("big")
	write(t, filepath.Join(w.Path, "a.txt"), "12345")
	write(t, filepath.Join(w.Path, "b.txt"), "67890") // 10 bytes together, cap 8
	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonOutsideFiles || !strings.Contains(got.Detail, "over the") {
		t.Fatalf("got %+v, want held/files-outside-checkout over the cap", got)
	}
	if !exists(filepath.Join(w.Path, "a.txt")) || exists(f.r.reclaimedRoot()) {
		t.Fatal("a held workspace keeps its files and nothing is saved")
	}
}

func TestRootFiles_NonRegularFileHolds(t *testing.T) {
	t.Parallel()
	if why := rootFilesUnsavable([]string{t.TempDir()}, DefaultRootFilesCap); why == "" {
		t.Fatal("a directory entry is not a savable file")
	}
	if why := rootFilesUnsavable([]string{filepath.Join(t.TempDir(), "missing")}, DefaultRootFilesCap); why == "" {
		t.Fatal("an unreadable entry is not savable")
	}
}

func TestRootFiles_CapIsAboutFiftyMegabytes(t *testing.T) {
	t.Parallel()
	if DefaultRootFilesCap != 50<<20 {
		t.Fatalf("DefaultRootFilesCap = %d", DefaultRootFilesCap)
	}
	if (&Reclaimer{}).rootFilesCap() != DefaultRootFilesCap || (&Reclaimer{RootFilesCap: 7}).rootFilesCap() != 7 {
		t.Fatal("rootFilesCap must default and honour the override")
	}
}

func TestCopyRootFile_NeverOverwritesADifferentSavedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "saved", "src.txt")
	write(t, src, "new\n")
	write(t, dst, "an earlier save\n")
	got, err := copyRootFile(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if got == dst {
		t.Fatal("a different file already saved there was overwritten")
	}
	if b, _ := os.ReadFile(dst); string(b) != "an earlier save\n" {
		t.Fatalf("earlier save changed: %q", b)
	}
	if b, _ := os.ReadFile(got); string(b) != "new\n" {
		t.Fatalf("copy = %q", b)
	}
	// An identical copy is reused, not duplicated.
	again, err := copyRootFile(src, got)
	if err != nil || again != got {
		t.Fatalf("identical copy: %q, %v", again, err)
	}
}

func TestRootFiles_AFileWrittenAfterItWasSavedStaysAndKeepsTheWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "ws", "NOTES.md")
	write(t, src, "saved\n")
	r := &Reclaimer{Root: filepath.Join(dir, "worktrees")}
	saved, err := r.saveRootFiles(Worktree{ID: "w", Path: filepath.Join(dir, "ws")}, []string{src})
	if err != nil {
		t.Fatal(err)
	}
	write(t, src, "written after the copy\n")
	saved.removeOriginals()
	if b, _ := os.ReadFile(src); string(b) != "written after the copy\n" {
		t.Fatalf("a file that changed since it was saved must stay: %q", b)
	}
}

// ---- secrets/dev.yaml ----------------------------------------------------

// devSecretCases are the secrets/dev.yaml comparisons: a worktree's copy against
// the source checkout's. wantHeld is whether the worktree holds a value the
// source lacks, which removal would lose.
var devSecretCases = []struct {
	name, source, worktree string
	wantHeld               bool
}{
	{"reordered and re-commented", "a: 1\nb: two\n", "# mine\nb: two\na: 1\n", false},
	{"whitespace and quoting only", "a: 1\nb: two\n", "a:   1\nb: \"two\"\n", false},
	{"empty values the source lacks", "a: 1\n", "a: 1\nnew_key: \"\"\nother:\n", false},
	{"a subset of the source", "a: 1\nb: 2\nc: 3\n", "a: 1\n", false},
	{"a new non-empty value", "a: 1\n", "a: 1\ntoken: abc\n", true},
	{"a changed value", "token: new\n", "token: old\n", true},
	{"nested new value", "db:\n  host: h\n", "db:\n  host: h\n  password: p\n", true},
	{"a list item that differs", "k:\n  - a\n  - b\n", "k:\n  - a\n  - c\n", true},
	{"not yaml", "a: 1\n", "a: [unterminated\n", true},
}

// The comparison itself, with no git: the -short counterpart of the fixture test
// below, which drives the same cases through a whole reclaim (~22 git processes
// each).
func TestYamlValuesIn_DevSecretCases(t *testing.T) {
	t.Parallel()
	for _, tc := range devSecretCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			source, worktree := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "worktree.yaml")
			write(t, source, tc.source)
			write(t, worktree, tc.worktree)
			if covered := yamlValuesIn(worktree, source); covered == tc.wantHeld {
				t.Fatalf("yamlValuesIn = %v, but wantHeld=%v", covered, tc.wantHeld)
			}
		})
	}
}

func TestDevSecrets_ProtectedOnlyForValuesTheSourceLacks(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "nine reclaims of ~22 git processes each; TestYamlValuesIn_DevSecretCases covers the comparison")
	for _, tc := range devSecretCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.idle()
			ignore(t, f, "secrets/\n")
			write(t, filepath.Join(f.project, "secrets", "dev.yaml"), tc.source)
			w := f.worktree("dev", "feat/dev")
			write(t, filepath.Join(w.Path, "secrets", "dev.yaml"), tc.worktree)
			got := f.one(w)
			if got.Outcome != OutcomeHeld || !exists(filepath.Join(w.Path, "secrets", "dev.yaml")) {
				t.Fatalf("automatic whole-worktree cleanup retains protected secrets regardless of copy equivalence: %+v", got)
			}
		})
	}
}

func TestDevSecrets_OnlyThatFileIsComparedByValue(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]bool{
		"secrets/dev.yaml":     true,
		"sub/secrets/dev.yaml": true,
		"secrets/prod.yaml":    false,
		"dev.yaml":             false,
		"secrets/dev.yml":      false,
	} {
		if got := devSecrets(rel); got != want {
			t.Errorf("devSecrets(%q) = %v, want %v", rel, got, want)
		}
	}
}
