// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// An ignored file that is a byte-identical copy of the source checkout's file
// at the same path is not unique data: removing the worktree loses nothing.
// That is how a worktree gets its dev secrets and .env, and it held every such
// worktree forever. Both paths remove it.
func TestReconcile_IgnoredCopyOfSourceDoesNotHold(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds 2 git fixtures and removes them; TestCopyInSource_RefusesPathsThatLeaveTheSource covers the rule under -short")
	}
	f := newFixture(t)
	f.idle()
	ignore(t, f, "secrets/\n.env\n")
	write(t, filepath.Join(f.project, "secrets", "dev.yaml"), "token: abc\n")
	write(t, filepath.Join(f.project, ".env"), "KEY=1\n")
	for name, play := range map[string]func(Worktree) Result{"auto": f.one, "confirmed": f.confirmed} {
		w := f.worktree("copy-"+name, "feat/copy-"+name)
		write(t, filepath.Join(w.Path, "secrets", "dev.yaml"), "token: abc\n")
		write(t, filepath.Join(w.Path, ".env"), "KEY=1\n")
		got := play(w)
		if name == "auto" {
			if got.Outcome != OutcomeHeld || !exists(filepath.Join(w.Path, ".env")) {
				t.Fatalf("automatic cleanup must preserve protected paths even when copied: %+v", got)
			}
		} else if got.Outcome != OutcomeRemoved || exists(w.Path) {
			t.Fatalf("%s: got %+v, want removed", name, got)
		}
	}
	// The source's copies are untouched.
	if b, err := os.ReadFile(filepath.Join(f.project, "secrets", "dev.yaml")); err != nil || string(b) != "token: abc\n" {
		t.Fatalf("source copy changed: %q, %v", b, err)
	}
}

// A copy that differs from the source's, even only by being an older version,
// may hold a credential the source no longer has; one the source does not have
// at all is the only copy. Both hold, on both paths.
func TestReconcile_IgnoredFileUnlikeSourceHolds(t *testing.T) {
	t.Parallel()
	testkit.Slow(t, "builds real git repositories and runs git against them")
	f := newFixture(t)
	f.idle()
	ignore(t, f, "secrets/\n")
	write(t, filepath.Join(f.project, "secrets", "dev.yaml"), "token: new\n")
	for name, rel := range map[string]string{
		"older":     "secrets/dev.yaml",
		"only-here": "secrets/prod.yaml",
	} {
		w := f.worktree("unlike-"+name, "feat/unlike-"+name)
		path := filepath.Join(w.Path, filepath.FromSlash(rel))
		write(t, path, "token: old\n")
		if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(path) {
			t.Fatalf("%s: got %+v, want held/data", name, got)
		}
		if got := f.confirmed(w); got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(path) {
			t.Fatalf("%s confirmed: got %+v, want held/data", name, got)
		}
	}
}

// The "copy" must be a separate file inside the source checkout. A source
// path that is a symlink back into the worktree reaches the very file being
// judged, and removing the worktree would delete the only copy.
func TestReconcile_SourceSymlinkIntoWorktreeIsNotACopy(t *testing.T) {
	t.Parallel()
	testkit.Slow(t, "builds real git repositories and runs git against them")
	f := newFixture(t)
	f.idle()
	ignore(t, f, "secrets\n")
	w := f.worktree("loop", "feat/loop")
	path := filepath.Join(w.Path, "secrets", "dev.yaml")
	write(t, path, "token: only\n")
	if err := os.Symlink(filepath.Join(w.Path, "secrets"), filepath.Join(f.project, "secrets")); err != nil {
		t.Fatal(err)
	}
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(path) {
		t.Fatalf("got %+v, want held/data", got)
	}
}

// copyInSource itself, without the quarantine move that also defeats the
// symlink above by leaving it dangling: a source path that leads back into the
// checkout, or out of the source checkout, is never a copy.
func TestCopyInSource_RefusesPathsThatLeaveTheSource(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkout, source, elsewhere := filepath.Join(base, "wt"), filepath.Join(base, "src"), filepath.Join(base, "other")
	write(t, filepath.Join(checkout, "secrets", "dev.yaml"), "token\n")
	write(t, filepath.Join(elsewhere, "secrets", "dev.yaml"), "token\n")
	write(t, filepath.Join(source, "plain", "dev.yaml"), "token\n")
	write(t, filepath.Join(checkout, "plain", "dev.yaml"), "token\n")
	if err := os.Symlink(filepath.Join(checkout, "secrets"), filepath.Join(source, "secrets")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "secrets"), filepath.Join(source, "away")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(checkout, "away", "dev.yaml"), "token\n")
	d := dataWalk{checkout: checkout, source: func() string { return source }}
	for rel, want := range map[string]bool{
		"plain/dev.yaml":   true,  // a separate file in the source
		"secrets/dev.yaml": false, // the source path is this very file
		"away/dev.yaml":    false, // the source path resolves outside the source
	} {
		full := filepath.Join(checkout, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.copyInSource(rel, full, info); got != want {
			t.Errorf("%s: got %v, want %v", rel, got, want)
		}
	}
}

// An ignored file a tool generated (its first line is the generated-code
// marker) is rebuilt by that tool: forge's runtime config.js. The never-list
// still wins, and the marker counts only on the first line.
func TestReconcile_GeneratedIgnoredFile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds 3 git fixtures; TestGeneratedMarker covers the rule under -short")
	}
	f := newFixture(t)
	f.idle()
	ignore(t, f, "public/config.js\n.env\nnotes.txt\n")
	const marker = "// Code generated by forge. DO NOT EDIT.\nwindow.__CONFIG__ = {};\n"
	w := f.worktree("gen", "feat/gen")
	write(t, filepath.Join(w.Path, "public", "config.js"), marker)
	if got := f.one(w); got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("generated: got %+v, want removed", got)
	}
	for name, file := range map[string]struct{ rel, content string }{
		"never-listed":   {".env", "# Code generated by a tool. DO NOT EDIT.\nSECRET=1\n"},
		"marker-not-top": {"notes.txt", "my notes\n// Code generated by forge. DO NOT EDIT.\n"},
	} {
		w := f.worktree("gen-"+name, "feat/gen-"+name)
		path := filepath.Join(w.Path, file.rel)
		write(t, path, file.content)
		if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(path) {
			t.Fatalf("%s: got %+v, want held/data", name, got)
		}
	}
}

// The walk stops at maxDataNames entries. Paths that are not data must not
// count toward it, or enough of them would hide a real data file behind the
// cap and let the worktree go.
func TestReconcile_CopiesDoNotFillTheDataCap(t *testing.T) {
	t.Parallel()
	testkit.Slow(t, "builds real git repositories and runs git against them")
	f := newFixture(t)
	f.idle()
	ignore(t, f, "secrets/\n")
	w := f.worktree("cap", "feat/cap")
	for i := range maxDataNames + 5 {
		name := fmt.Sprintf("a%02d.yaml", i)
		write(t, filepath.Join(f.project, "secrets", name), "same\n")
		write(t, filepath.Join(w.Path, "secrets", name), "same\n")
	}
	path := filepath.Join(w.Path, "secrets", "zz-real.yaml")
	write(t, path, "token: only-here\n")
	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonData || !exists(path) {
		t.Fatalf("got %+v, want held/data", got)
	}
	if !strings.Contains(got.Detail, "zz-real.yaml") {
		t.Fatalf("detail %q does not name the data file", got.Detail)
	}
}

// A symlink inside a checkout is that checkout's file wherever it points. A
// venv's bin/python3 -> /usr/bin/python3.12 was reported as a file outside
// every checkout, which holds the workspace for good. The target survives.
func TestReconcile_SymlinkOutOfCheckoutIsInsideIt(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~36 git processes")
	f := newFixture(t)
	f.idle()
	ignore(t, f, ".tools/\n")
	target := filepath.Join(filepath.Dir(f.root), "python3.12")
	write(t, target, "#!/bin/true\n")
	w := f.worktree("venv", "feat/venv")
	link := filepath.Join(w.Path, ".tools", "venv", "bin", "python3")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := f.one(w); got.Outcome != OutcomeHeld || !exists(link) {
		t.Fatalf("uncertain symlink layout must remain intact: %+v", got)
	}
	if !exists(target) {
		t.Fatal("the symlink's target was deleted")
	}
}

func TestGeneratedMarker(t *testing.T) {
	for line, want := range map[string]bool{
		"// Code generated by forge. DO NOT EDIT.":             true,
		"// Code generated by protoc-gen-go. DO NOT EDIT.":     true,
		"# Code generated by sqlc. DO NOT EDIT.":               true,
		"/* Code generated by a tool. DO NOT EDIT. */":         true,
		"<!-- Code generated by a tool. DO NOT EDIT. -->":      true,
		"-- Code generated by a tool. DO NOT EDIT.":            true,
		"// Code generated by forge.":                          false,
		"// generated by forge, DO NOT EDIT.":                  false,
		"const note = 'Code generated by forge. DO NOT EDIT.'": false,
		"token: abc # Code generated by forge. DO NOT EDIT.":   false,
		"": false,
	} {
		if got := generatedMarker.MatchString(line); got != want {
			t.Errorf("%q: got %v, want %v", line, got, want)
		}
	}
}

func TestSameBytes(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 200<<10)
	for name, c := range map[string]struct {
		a, b string
		want bool
	}{
		"equal":         {"abc\n", "abc\n", true},
		"empty":         {"", "", true},
		"differ":        {"abc\n", "abd\n", false},
		"prefix":        {"abc", "abcd", false},
		"big-equal":     {big, big, true},
		"big-last-byte": {big + "a", big + "b", false},
	} {
		a, b := filepath.Join(dir, name+".a"), filepath.Join(dir, name+".b")
		write(t, a, c.a)
		write(t, b, c.b)
		if got := sameBytes(a, b); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
	if sameBytes(filepath.Join(dir, "missing"), filepath.Join(dir, "equal.a")) {
		t.Error("a missing file compared equal")
	}
}
