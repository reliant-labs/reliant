// Package mergeproperty checks forge's merge invariant for one generated file:
// two branches that change independent inputs merge with plain `git merge` —
// no merge driver, no attributes, exactly as GitHub's server-side merge does —
// into the bytes forge generate writes for the merged inputs.
//
// A generator that aggregates independent parts (all tables' grants, all
// config blocks, all services' handler types) holds the invariant by
// certifying each part separately (checksums' sectioned self-certification)
// and by keeping an unchanged line between parts, so the only lines two such
// branches share are lines neither changes. Each generator's test states its
// inputs as four variants — base, ours, theirs (independent changes to base)
// and merged (both) — and calls AssertPlainMergeIsRender.
package mergeproperty

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
)

// Variants are the four input states a merge-property case renders.
var Variants = []string{"base", "ours", "theirs", "merged"}

// AssertPlainMergeIsRender stamps render("base"), render("ours") and
// render("theirs") the way forge's writer does, merges theirs into ours with
// plain git over base, and fails unless the result is byte-identical to the
// stamped render("merged") and certifies itself.
func AssertPlainMergeIsRender(t *testing.T, rel string, render func(variant string) []byte) {
	t.Helper()
	stamp := func(v string) []byte {
		t.Helper()
		out, ok := checksums.Stamp(rel, render(v))
		if !ok {
			t.Fatalf("%s is not stampable", rel)
		}
		return out
	}
	got := PlainMerge(t, rel, stamp("base"), stamp("ours"), stamp("theirs"))
	want := stamp("merged")
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: a plain git merge of independent changes differs from forge generate on the merged inputs:\n--- merged ---\n%s\n--- generate ---\n%s", rel, got, want)
	}
	if checksums.Verify(got) != checksums.Pristine {
		t.Errorf("%s: the merged file must certify itself", rel)
	}
}

// PlainMerge commits base at rel, then ours and theirs on two branches, and
// returns the file after a plain `git merge` of theirs into ours. A conflict
// fails the test, naming the file.
func PlainMerge(t *testing.T, rel string, base, ours, theirs []byte) []byte {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		// No user or system config: no merge driver, no attributes.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			conflicted, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			t.Fatalf("git %s: %v\n%s\n--- %s ---\n%s", strings.Join(args, " "), err, out, rel, conflicted)
		}
	}
	write := func(b []byte) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := atomicfile.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write(base)
	git("add", ".")
	git("commit", "-qm", "base")
	git("checkout", "-qb", "theirs")
	write(theirs)
	git("commit", "-qam", "theirs")
	git("checkout", "-q", "main")
	write(ours)
	git("commit", "-qam", "ours")
	git("merge", "-q", "--no-edit", "theirs")
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// AssertPlainMergeIsRenderTree is AssertPlainMergeIsRender for output that is
// a TREE of files (one file per input, like the scaffold ledger): build
// brings the work tree dir to the given variant's state through forge's own
// writers, starting from whatever is there. The files under sub after a
// plain merge of the "ours" and "theirs" commits must be exactly the files
// build writes for "merged" into an empty directory.
func AssertPlainMergeIsRenderTree(t *testing.T, sub string, build func(dir, variant string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q", "-b", "main")
	build(dir, "base")
	git("add", "-A")
	git("commit", "-qm", "base", "--allow-empty")
	git("checkout", "-qb", "theirs")
	build(dir, "theirs")
	git("add", "-A")
	git("commit", "-qm", "theirs", "--allow-empty")
	git("checkout", "-q", "main")
	build(dir, "ours")
	git("add", "-A")
	git("commit", "-qm", "ours", "--allow-empty")
	git("merge", "-q", "--no-edit", "theirs")

	fresh := t.TempDir()
	build(fresh, "merged")
	got, want := treeFiles(t, filepath.Join(dir, sub)), treeFiles(t, filepath.Join(fresh, sub))
	for rel, body := range want {
		if got[rel] != body {
			t.Errorf("%s/%s after a plain git merge differs from forge's writers on the merged inputs:\n--- merged ---\n%s\n--- generate ---\n%s", sub, rel, got[rel], body)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("%s/%s survives the merge but forge's writers on the merged inputs do not write it", sub, rel)
		}
	}
}

// treeFiles maps every file under dir (slash-relative) to its content.
func treeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}
