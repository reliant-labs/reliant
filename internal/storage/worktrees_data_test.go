package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// reapAged ages the worktree, stubs lsof, and runs one applying pass.
func reapAged(t *testing.T, repo, worktree string) WorktreeReport {
	t.Helper()
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	return reapOnce(t, Runner{}, repo, true)
}

func wantRemoved(t *testing.T, report WorktreeReport, worktree string) {
	t.Helper()
	if _, err := os.Stat(worktree); err == nil || len(report.Removed) != 1 {
		t.Fatalf("want removed, got removed=%v held=%+v", report.Removed, report.Held)
	}
}

func wantHeldData(t *testing.T, report WorktreeReport, worktree string) WorktreeHold {
	t.Helper()
	if got := heldReason(report, worktree); got != HoldData {
		t.Fatalf("held reason = %q, want %q (removed %v, held %+v)", got, HoldData, report.Removed, report.Held)
	}
	for _, h := range report.Held {
		if h.Reason == HoldData {
			return h
		}
	}
	return WorktreeHold{}
}

// An ignored file byte-identical to the main checkout's file at the same path
// is not unique data: removing the worktree loses nothing. That is how a
// worktree gets its dev secrets and .env, and it held every such worktree.
func TestWorktreeIgnoredCopyOfMainCheckoutDoesNotHold(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	excludeInRepo(t, repo, "secrets/\n")
	writeFile(t, filepath.Join(repo, "secrets", "dev.yaml"), "token: abc\n")
	writeFile(t, filepath.Join(repo, ".env"), "KEY=1\n")
	writeFile(t, filepath.Join(worktree, "secrets", "dev.yaml"), "token: abc\n")
	writeFile(t, filepath.Join(worktree, ".env"), "KEY=1\n")
	wantRemoved(t, reapAged(t, repo, worktree), worktree)
	if b, err := os.ReadFile(filepath.Join(repo, "secrets", "dev.yaml")); err != nil || string(b) != "token: abc\n" {
		t.Fatalf("main checkout's copy changed: %q, %v", b, err)
	}
}

// A copy that differs, even only by being an older version, may hold a
// credential the main checkout no longer has; one the main checkout does not
// have at all is the only copy. Both hold.
func TestWorktreeIgnoredFileUnlikeMainCheckoutHolds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	for name, rel := range map[string]string{
		"older":     "secrets/dev.yaml",
		"only-here": "secrets/prod.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			excludeInRepo(t, repo, "secrets/\n")
			writeFile(t, filepath.Join(repo, "secrets", "dev.yaml"), "token: new\n")
			path := filepath.Join(worktree, filepath.FromSlash(rel))
			writeFile(t, path, "token: old\n")
			hold := wantHeldData(t, reapAged(t, repo, worktree), worktree)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s was deleted: %v", rel, err)
			}
			if !strings.Contains(hold.Detail, rel) || !strings.Contains(hold.Detail, "not in "+canonicalPath(repo)+", or different there") {
				t.Fatalf("detail %q does not name the file and say how to free it", hold.Detail)
			}
		})
	}
}

// The "copy" must be a separate file inside the main checkout. A main-checkout
// path that is a symlink back into the worktree reaches the very file being
// judged, and removing the worktree would delete the only copy.
func TestWorktreeMainCheckoutSymlinkIntoWorktreeIsNotACopy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	excludeInRepo(t, repo, "secrets\n")
	path := filepath.Join(worktree, "secrets", "dev.yaml")
	writeFile(t, path, "token: only\n")
	if err := os.Symlink(filepath.Join(worktree, "secrets"), filepath.Join(repo, "secrets")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ageDir(t, worktree)
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	wantHeldData(t, reapOnce(t, Runner{}, repo, false), worktree)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// copyInSource itself: a main-checkout path that leads back into the
// worktree, out of the main checkout, or is the same file is never a copy.
func TestCopyInSourceRefusesPathsThatLeaveTheMainCheckout(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The worktree lives inside the main checkout, as with a .worktrees/ dir.
	source := filepath.Join(base, "src")
	checkout := filepath.Join(source, "wt")
	elsewhere := filepath.Join(base, "other")
	writeFile(t, filepath.Join(source, "plain", "dev.yaml"), "token\n")
	writeFile(t, filepath.Join(checkout, "plain", "dev.yaml"), "token\n")
	writeFile(t, filepath.Join(checkout, "secrets", "dev.yaml"), "token\n")
	writeFile(t, filepath.Join(checkout, "away", "dev.yaml"), "token\n")
	writeFile(t, filepath.Join(checkout, "inside", "dev.yaml"), "token\n")
	writeFile(t, filepath.Join(elsewhere, "away", "dev.yaml"), "token\n")
	writeFile(t, filepath.Join(checkout, "real", "dev.yaml"), "token\n")
	for link, target := range map[string]string{
		"secrets": filepath.Join(checkout, "secrets"), // this very file
		"away":    filepath.Join(elsewhere, "away"),   // outside the main checkout
		"inside":  filepath.Join(checkout, "real"),    // a different file, but inside the worktree
	} {
		if err := os.Symlink(target, filepath.Join(source, link)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	// A hard link is the same file under another name. Judging it a copy
	// would be harmless, but "a separate file" is the rule, so it holds.
	if err := os.Link(filepath.Join(checkout, "plain", "dev.yaml"), filepath.Join(source, "linked.yaml")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := os.Link(filepath.Join(checkout, "plain", "dev.yaml"), filepath.Join(checkout, "linked.yaml")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	j := dataJudge{checkout: checkout, source: source}
	for rel, want := range map[string]bool{
		"plain/dev.yaml":   true,
		"secrets/dev.yaml": false,
		"away/dev.yaml":    false,
		"inside/dev.yaml":  false,
		"linked.yaml":      false,
	} {
		full := filepath.Join(checkout, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatal(err)
		}
		if got := j.copyInSource(rel, full, info); got != want {
			t.Errorf("%s: got %v, want %v", rel, got, want)
		}
	}
	if (&dataJudge{checkout: checkout}).copyInSource("plain/dev.yaml", filepath.Join(checkout, "plain", "dev.yaml"), mustLstat(t, filepath.Join(checkout, "plain", "dev.yaml"))) {
		t.Error("a repository with no main working tree has no copy to compare with")
	}
}

func mustLstat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// An ignored file a tool generated (its first line is the generated-code
// marker) is rebuilt by that tool: forge's runtime config.js. The never-list
// still wins, and the marker counts only on the first line.
func TestWorktreeGeneratedIgnoredFile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	const marker = "// Code generated by forge. DO NOT EDIT.\nwindow.__CONFIG__ = {};\n"
	t.Run("generated", func(t *testing.T) {
		repo, worktree := gitRepoWithWorktree(t)
		excludeInRepo(t, repo, "public/config.js\n")
		writeFile(t, filepath.Join(worktree, "public", "config.js"), marker)
		wantRemoved(t, reapAged(t, repo, worktree), worktree)
	})
	for name, file := range map[string]struct{ rel, content string }{
		"never-listed":   {".env", "# Code generated by a tool. DO NOT EDIT.\nSECRET=1\n"},
		"marker-not-top": {"notes.txt", "my notes\n// Code generated by forge. DO NOT EDIT.\n"},
	} {
		t.Run(name, func(t *testing.T) {
			repo, worktree := gitRepoWithWorktree(t)
			excludeInRepo(t, repo, "notes.txt\n")
			path := filepath.Join(worktree, file.rel)
			writeFile(t, path, file.content)
			wantHeldData(t, reapAged(t, repo, worktree), worktree)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s was deleted: %v", file.rel, err)
			}
		})
	}
}

// The walk stops at maxDataNames entries. Paths that are not data must not
// count toward it, or enough of them would hide a real data file behind the
// cap and let the worktree go.
func TestWorktreeCopiesDoNotFillTheDataCap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo, worktree := gitRepoWithWorktree(t)
	excludeInRepo(t, repo, "secrets/\n")
	for i := range maxDataNames + 5 {
		name := "a" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".yaml"
		writeFile(t, filepath.Join(repo, "secrets", name), "same\n")
		writeFile(t, filepath.Join(worktree, "secrets", name), "same\n")
	}
	path := filepath.Join(worktree, "secrets", "zz-real.yaml")
	writeFile(t, path, "token: only-here\n")
	hold := wantHeldData(t, reapAged(t, repo, worktree), worktree)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hold.Detail, "zz-real.yaml") {
		t.Fatalf("detail %q does not name the data file", hold.Detail)
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

func TestGeneratedFileReadsOnlyTheFirstLine(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		content string
		want    bool
	}{
		"first":    {"// Code generated by forge. DO NOT EDIT.\nbody\n", true},
		"crlf":     {"// Code generated by forge. DO NOT EDIT.\r\nbody\r\n", true},
		"second":   {"body\n// Code generated by forge. DO NOT EDIT.\n", false},
		"empty":    {"", false},
		"no-break": {strings.Repeat("x", 2000) + "// Code generated by forge. DO NOT EDIT.", false},
	} {
		p := filepath.Join(dir, name)
		writeFile(t, p, c.content)
		if got := generatedFile(p); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
	if generatedFile(filepath.Join(dir, "missing")) {
		t.Error("a missing file is not generated")
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
		writeFile(t, a, c.a)
		writeFile(t, b, c.b)
		if got := sameBytes(a, b); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
	if sameBytes(filepath.Join(dir, "missing"), filepath.Join(dir, "equal.a")) {
		t.Error("a missing file compared equal")
	}
}

func TestNeverRebuildablePath(t *testing.T) {
	for p, want := range map[string]bool{
		".env": true, "secrets/dev.yaml": true, "x/data/": true, "a.db": true,
		"public/config.js": false, "node_modules/": false,
	} {
		if got := neverRebuildablePath(p); got != want {
			t.Errorf("neverRebuildablePath(%q) = %v, want %v", p, got, want)
		}
	}
}

// A file over maxCompareBytes is never compared: it holds, even when it is
// identical to the main checkout's. Sparse files keep this cheap.
func TestCopyInSourceRefusesFilesOverTheCompareCap(t *testing.T) {
	base := t.TempDir()
	checkout, source := filepath.Join(base, "wt"), filepath.Join(base, "src")
	for _, size := range []int64{maxCompareBytes, maxCompareBytes + 1} {
		for _, root := range []string{checkout, source} {
			p := filepath.Join(root, "big.bin")
			writeFile(t, p, "")
			if err := os.Truncate(p, size); err != nil {
				t.Skipf("sparse files unavailable: %v", err)
			}
		}
		full := filepath.Join(checkout, "big.bin")
		j := dataJudge{checkout: checkout, source: source}
		if got, want := j.copyInSource("big.bin", full, mustLstat(t, full)), size == maxCompareBytes; got != want {
			t.Errorf("size %d: got %v, want %v", size, got, want)
		}
	}
}
