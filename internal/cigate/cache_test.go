package cigate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a git repository in a temp dir with one commit of files.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for rel, body := range files {
		write(t, dir, rel, body)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func openCache(t *testing.T, root, store, forge string) (*VerdictCache, string) {
	t.Helper()
	return OpenVerdictCache(context.Background(), CacheInput{Root: root, Dir: store, Forge: forge, Env: append(os.Environ(), "GOWORK=off")})
}

// A PASS recorded on a clean tree answers for the same tree later — in this
// worktree or another one of the same repository — and for nothing else.
func TestVerdictCache_KeyedByTreeAndShared(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git repositories and `go env`; runs in task test")
	}
	root := gitRepo(t, map[string]string{"a.txt": "a", ".gitignore": "tmp/\n"})
	store := t.TempDir()
	lint := check("lint", "golangci-lint", "run")

	v, why := openCache(t, root, store, "forge v1")
	if v == nil {
		t.Fatalf("clean tree: cache off: %s", why)
	}
	if _, ok := v.Lookup(lint); ok {
		t.Fatal("an empty cache answered")
	}
	v.Record(lint)
	if n, why := v.Flush(context.Background()); n != 1 || why != "" {
		t.Fatalf("Flush = %d, %q", n, why)
	}

	again, _ := openCache(t, root, store, "forge v1")
	if commit, ok := again.Lookup(lint); !ok || commit == "" {
		t.Fatal("the same tree did not hit")
	}
	// The key covers the check's command, env and tools, not just its name.
	for _, changed := range []Check{
		check("lint", "golangci-lint", "run", "--fix"),
		func() Check { c := lint; c.Env = []string{"X=1"}; return c }(),
		func() Check { c := lint; c.Tools = []Tool{{Binary: "golangci-lint", Version: "v9"}}; return c }(),
		check("lint2", "golangci-lint", "run"),
	} {
		if _, ok := again.Lookup(changed); ok {
			t.Errorf("%+v hit a verdict recorded for a different check", changed)
		}
	}
	// Another forge (or another pin set) is another judge.
	if other, _ := openCache(t, root, store, "forge v2"); other == nil {
		t.Fatal("cache off")
	} else if _, ok := other.Lookup(lint); ok {
		t.Error("a verdict from another forge answered")
	}

	// Another worktree of the repository, on the same tree, shares it.
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "--detach", wt, "HEAD")
	if other, why := openCache(t, wt, store, "forge v1"); other == nil {
		t.Fatalf("worktree: %s", why)
	} else if _, ok := other.Lookup(lint); !ok {
		t.Error("the same tree in another worktree missed")
	}

	// A commit that changes no file keeps the tree, and the verdict.
	git(t, root, "commit", "-q", "--allow-empty", "-m", "empty")
	if v, _ := openCache(t, root, store, "forge v1"); v == nil {
		t.Fatal("cache off")
	} else if _, ok := v.Lookup(lint); !ok {
		t.Error("an empty commit lost the verdict: it is keyed by tree, not commit")
	}
	// A commit that changes a file is a new tree.
	write(t, root, "a.txt", "b")
	git(t, root, "commit", "-q", "-am", "change")
	if v, _ := openCache(t, root, store, "forge v1"); v == nil {
		t.Fatal("cache off")
	} else if _, ok := v.Lookup(lint); ok {
		t.Error("a changed tree hit the old verdict")
	}
}

// A dirty tree has no hash, so the cache is off for the run and says why; a
// tree that changes during the run stores nothing.
func TestVerdictCache_DirtyTreeIsOff(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git repositories and `go env`; runs in task test")
	}
	root := gitRepo(t, map[string]string{"a.txt": "a"})
	store := t.TempDir()
	v, _ := openCache(t, root, store, "f")
	v.Record(check("lint", "lint"))

	write(t, root, "a.txt", "edited")
	if n, why := v.Flush(context.Background()); n != 0 || !strings.Contains(why, "changed during the run") {
		t.Errorf("Flush after the tree changed = %d, %q", n, why)
	}
	if v, why := openCache(t, root, store, "f"); v != nil || !strings.Contains(why, "uncommitted") {
		t.Errorf("dirty tree: cache=%v why=%q", v != nil, why)
	}
}

// A local go.mod replace reads a sibling checkout the tree hash does not
// cover: it must be a clean git tree, whose own hash joins the key.
func TestVerdictCache_OutsideInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git repositories and `go env`; runs in task test")
	}
	sibling := gitRepo(t, map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n"})
	root := gitRepo(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => " + sibling + "\n",
	})
	store := t.TempDir()
	c := check("build", "go", "build", "./...")

	v, why := openCache(t, root, store, "f")
	if v == nil {
		t.Fatalf("clean sibling: %s", why)
	}
	v.Record(c)
	if n, _ := v.Flush(context.Background()); n != 1 {
		t.Fatal("not stored")
	}

	write(t, sibling, "lib.go", "package lib\n\nvar X = 1\n")
	if v, why := openCache(t, root, store, "f"); v != nil || !strings.Contains(why, "uncommitted") {
		t.Fatalf("dirty sibling: cache=%v why=%q", v != nil, why)
	}
	git(t, sibling, "commit", "-q", "-am", "change")
	if v, _ := openCache(t, root, store, "f"); v == nil {
		t.Fatal("clean sibling: cache off")
	} else if _, ok := v.Lookup(c); ok {
		t.Error("a verdict survived a change in the sibling it built against")
	}

	notGit := t.TempDir()
	write(t, notGit, "go.mod", "module example.com/other\n")
	root2 := gitRepo(t, map[string]string{"go.mod": "module example.com/app2\n\nreplace example.com/other => " + notGit + "\n"})
	if v, why := openCache(t, root2, store, "f"); v != nil || !strings.Contains(why, "not in a git work tree") {
		t.Errorf("non-git replace: cache=%v why=%q", v != nil, why)
	}
}
