// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// dirtyCheckout makes a linked worktree holding every kind of work a capture
// must keep: an unpushed commit, a commit only the reflog reaches, a staged
// edit with a further unstaged edit on top, an unstaged edit, a staged new
// file, a staged deletion and an untracked file. It returns the path, the
// unpushed HEAD and the reflog-only commit.
func dirtyCheckout(t *testing.T, f *fixture, path, branch string) (head, orphan string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.project, "b.txt"), "bee\n")
	write(t, filepath.Join(f.project, "gone.txt"), "soon gone\n")
	run(t, f.project, "add", ".")
	run(t, f.project, "commit", "-q", "-m", "two more files")
	run(t, f.project, "push", "-q", "origin", "main")
	run(t, f.project, "worktree", "add", "-q", "-b", branch, path, "main")

	write(t, filepath.Join(path, "a.txt"), "unpushed\n")
	run(t, path, "commit", "-q", "-am", "unpushed work")
	head = run(t, path, "rev-parse", "HEAD")
	write(t, filepath.Join(path, "a.txt"), "abandoned\n")
	run(t, path, "commit", "-q", "-am", "abandoned by a reset")
	orphan = run(t, path, "rev-parse", "HEAD")
	run(t, path, "reset", "-q", "--hard", head)

	write(t, filepath.Join(path, "a.txt"), "staged\n")
	run(t, path, "add", "a.txt")
	write(t, filepath.Join(path, "a.txt"), "staged then edited\n")
	write(t, filepath.Join(path, "b.txt"), "unstaged\n")
	write(t, filepath.Join(path, "new.txt"), "staged new file\n")
	run(t, path, "add", "new.txt")
	run(t, path, "rm", "-q", "gone.txt")
	write(t, filepath.Join(path, "notes", "untracked.md"), "untracked\n")
	return head, orphan
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertDirtyRestored checks a checkout restored from dirtyCheckout's
// capture holds exactly what it held, staged and unstaged apart.
func assertDirtyRestored(t *testing.T, path, head string) {
	t.Helper()
	if got := run(t, path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want the captured %s", got, head)
	}
	for rel, want := range map[string]string{"a.txt": "staged then edited\n", "b.txt": "unstaged\n", "new.txt": "staged new file\n", "notes/untracked.md": "untracked\n"} {
		if got := readFile(t, filepath.Join(path, rel)); got != want {
			t.Fatalf("%s on disk = %q, want %q", rel, got, want)
		}
	}
	if exists(filepath.Join(path, "gone.txt")) {
		t.Fatal("gone.txt was deleted (staged) and came back")
	}
	if got := run(t, path, "show", ":a.txt"); got != "staged" {
		t.Fatalf("staged a.txt = %q, want %q", got, "staged")
	}
	status := run(t, path, "status", "--porcelain", "--untracked-files=all")
	for _, want := range []string{"MM a.txt", " M b.txt", "A  new.txt", "D  gone.txt", "?? notes/untracked.md"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
}

// A capture keeps every kind of work, and a restore puts each back the way
// it was: the commit only the reflog reached stays reachable from the
// capture, staged changes are staged again, unstaged ones unstaged,
// untracked files untracked, and the branch is reattached.
func TestCaptureRestoreRoundTrip(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~40 git processes")
	f := newFixture(t)
	path := filepath.Join(filepath.Dir(f.project), "tmp", "agent-wt")
	head, orphan := dirtyCheckout(t, f, path, "feat/capture")
	ctx := context.Background()
	key := CaptureKey(path)

	c, err := captureCheckout(ctx, path, key, CaptureMeta{Path: path, Branch: "feat/capture"}, CaptureLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Ref, CaptureRefRoot+key+"/") {
		t.Fatalf("capture ref %s is not under %s", c.Ref, CaptureRefRoot+key)
	}
	if c.Head != head || !slices.Contains(c.Orphans, orphan) {
		t.Fatalf("capture head %s orphans %v, want head %s and orphan %s", c.Head, c.Orphans, head, orphan)
	}
	if err := verifyCapture(ctx, path, c); err != nil {
		t.Fatalf("an untouched checkout must verify: %v", err)
	}
	// Taking the capture changed nothing in the checkout.
	assertDirtyRestored(t, path, head)

	// The checkout is gone, its admin directory (and reflog) with it.
	run(t, f.project, "worktree", "remove", "--force", path)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the checkout is still there")
	}
	run(t, f.project, "merge-base", "--is-ancestor", orphan, c.Ref)

	res, err := RestoreCapture(ctx, RestoreOptions{Repo: f.project, Ref: c.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != path || res.Branch != "feat/capture" || res.BranchNote != "" {
		t.Fatalf("restore = %+v, want %s on feat/capture", res, path)
	}
	if !slices.Contains(res.Orphans, orphan) {
		t.Fatalf("restore did not name the reflog-only commit: %+v", res)
	}
	assertDirtyRestored(t, path, head)
	if got := run(t, path, "symbolic-ref", "HEAD"); got != "refs/heads/feat/capture" {
		t.Fatalf("HEAD names %s", got)
	}

	// The capture is read back as what it is, and it is kept.
	info, err := ReadCapture(ctx, f.project, c.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.Path != path || info.Meta.Key != key || info.Clean {
		t.Fatalf("read back %+v", info)
	}
}

// A capture is checked against the files immediately before they are
// deleted: any change made after it was taken, to a file, the index or the
// history, is a mismatch.
func TestVerifyCaptureRefusesAnyChange(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~60 git processes")
	f := newFixture(t)
	path := filepath.Join(filepath.Dir(f.project), "tmp", "verify-wt")
	dirtyCheckout(t, f, path, "feat/verify")
	ctx := context.Background()
	c, err := captureCheckout(ctx, path, CaptureKey(path), CaptureMeta{Path: path}, CaptureLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		change, undo func()
	}{
		{"a new untracked file", func() { write(t, filepath.Join(path, "late.txt"), "x") }, func() { os.Remove(filepath.Join(path, "late.txt")) }},
		{"an edit", func() { write(t, filepath.Join(path, "b.txt"), "edited later\n") }, func() { write(t, filepath.Join(path, "b.txt"), "unstaged\n") }},
		{"a staging", func() { run(t, path, "add", "b.txt") }, func() { run(t, path, "reset", "-q", "--", "b.txt") }},
	} {
		tc.change()
		if err := verifyCapture(ctx, path, c); !errors.Is(err, errCaptureMismatch) {
			t.Fatalf("%s: verify = %v, want a mismatch", tc.name, err)
		}
		tc.undo()
		if err := verifyCapture(ctx, path, c); err != nil {
			t.Fatalf("%s undone: verify = %v", tc.name, err)
		}
	}
	run(t, path, "commit", "-q", "-m", "a commit after the capture")
	if err := verifyCapture(ctx, path, c); !errors.Is(err, errCaptureMismatch) {
		t.Fatalf("a new commit: verify = %v, want a mismatch", err)
	}
}

// worktree.ensure recreates a reclaimed Reliant workspace from its branch,
// then applies its capture: only onto the captured commit, and once.
func TestApplyCaptureOntoRecreatedCheckout(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~50 git processes")
	f := newFixture(t)
	path := filepath.Join(f.root, "proj", "ws-1", "repo")
	head, _ := dirtyCheckout(t, f, path, "feat/ensure")
	ctx := context.Background()
	c, err := captureCheckout(ctx, path, "row-1", CaptureMeta{Path: path, Branch: "feat/ensure", RowID: "row-1"}, CaptureLimits{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "worktree", "remove", "--force", path)
	run(t, f.project, "worktree", "add", "-q", path, "feat/ensure")

	info, ok := LatestCapture(ctx, f.project, "row-1", path)
	if !ok || info.Ref != c.Ref {
		t.Fatalf("latest capture = %+v %v, want %s", info, ok, c.Ref)
	}
	if err := ApplyCapture(ctx, path, info); err != nil {
		t.Fatal(err)
	}
	assertDirtyRestored(t, path, head)
	if _, ok := LatestCapture(ctx, f.project, "row-1", path); ok {
		t.Fatal("an applied capture is still offered for the next restore")
	}
	run(t, f.project, "rev-parse", "--verify", RestoredCaptureRefRoot+strings.TrimPrefix(c.Ref, CaptureRefRoot))

	// A capture is never applied over a HEAD that moved.
	c2, err := captureCheckout(ctx, path, "row-1", CaptureMeta{Path: path, CapturedAt: time.Now()}, CaptureLimits{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "worktree", "remove", "--force", path)
	run(t, f.project, "worktree", "add", "-q", "--detach", path, "main")
	info2, _ := ReadCapture(ctx, f.project, c2.Ref)
	if err := ApplyCapture(ctx, path, info2); !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("apply over another commit = %v, want ErrBranchMoved", err)
	}
	if out := run(t, path, "status", "--porcelain"); out != "" {
		t.Fatalf("a refused apply touched the checkout:\n%s", out)
	}
}

// A new file staged with `git add -f` over an ignore rule, then edited
// further, is captured with its on-disk content and comes back on disk and
// staged: the files-on-disk tree is built from the index, not from HEAD, so
// an ignored path the index tracks is never dropped.
func TestCaptureKeepsAForceAddedIgnoredFile(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~30 git processes")
	f := newFixture(t)
	ignore(t, f, "*.log\n")
	path := filepath.Join(filepath.Dir(f.project), "tmp", "forced-wt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "worktree", "add", "-q", "-b", "feat/forced", path, "main")
	write(t, filepath.Join(path, "keep.log"), "staged\n")
	run(t, path, "add", "-f", "keep.log")
	write(t, filepath.Join(path, "keep.log"), "staged, then edited\n")
	ctx := context.Background()

	c, err := captureCheckout(ctx, path, CaptureKey(path), CaptureMeta{Path: path, Branch: "feat/forced"}, CaptureLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, f.project, "show", c.Ref+":keep.log"); got != "staged, then edited" {
		t.Fatalf("captured keep.log = %q, want the edited content on disk", got)
	}
	if err := verifyCapture(ctx, path, c); err != nil {
		t.Fatalf("an untouched checkout must verify: %v", err)
	}
	write(t, filepath.Join(path, "keep.log"), "edited after the capture\n")
	if err := verifyCapture(ctx, path, c); !errors.Is(err, errCaptureMismatch) {
		t.Fatalf("an edit to the force-added file: verify = %v, want a mismatch", err)
	}
	write(t, filepath.Join(path, "keep.log"), "staged, then edited\n")

	run(t, f.project, "worktree", "remove", "--force", path)
	if _, err := RestoreCapture(ctx, RestoreOptions{Repo: f.project, Ref: c.Ref}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(path, "keep.log")); got != "staged, then edited\n" {
		t.Fatalf("restored keep.log on disk = %q", got)
	}
	if got := run(t, path, "show", ":keep.log"); got != "staged" {
		t.Fatalf("restored staged keep.log = %q, want %q", got, "staged")
	}
}

// An approval's fingerprint is bound to content, not to status codes and the
// newest mtime: an edit to a file that is already dirty, or a different
// staged content, changes it even when every status code and every mtime
// (here a future-dated file is the newest) is kept.
func TestFingerprintBindsContent(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~30 git processes")
	f := newFixture(t)
	path := filepath.Join(filepath.Dir(f.project), "tmp", "fp-wt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "worktree", "add", "-q", "-b", "feat/fp", path, "main")
	ctx := context.Background()
	future := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	write(t, filepath.Join(path, "build.out"), "future-dated\n")
	if err := os.Chtimes(filepath.Join(path, "build.out"), future, future); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(path, "a.txt")
	set := func(content string) {
		write(t, a, content)
		if err := os.Chtimes(a, past, past); err != nil {
			t.Fatal(err)
		}
	}
	fp := func() string {
		got, err := fingerprint(ctx, path, path, scanCheckout(ctx, path).Newest)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	set("staged one\n")
	run(t, path, "add", "a.txt")
	set("on disk one\n")
	first := fp()
	status := run(t, path, "status", "--porcelain", "--untracked-files=all")
	if fp() != first {
		t.Fatal("the fingerprint of an unchanged checkout is not stable")
	}

	set("on disk two\n")
	if fp() == first {
		t.Fatal("an edit to an already-dirty file left the fingerprint unchanged")
	}
	set("on disk one\n")
	if fp() != first {
		t.Fatal("undoing the edit did not restore the fingerprint")
	}

	// A different length from "staged one\n": git add trusts its stat cache, and
	// with the mtime pinned, the same size and a ctime in the same second it
	// would keep the old blob in the index (racy git), so the test, not the
	// fingerprint, would see no change.
	set("staged second\n")
	run(t, path, "add", "a.txt")
	set("on disk one\n")
	if got := run(t, path, "status", "--porcelain", "--untracked-files=all"); got != status {
		t.Fatalf("the status changed (%q, was %q); the test needs it kept", got, status)
	}
	if fp() == first {
		t.Fatal("different staged content with the same status codes left the fingerprint unchanged")
	}
}
