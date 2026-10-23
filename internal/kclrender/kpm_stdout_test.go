package kclrender_test

// kpm's client writes its own progress ("cloning ...", "downloading ...",
// "waiting for package-cache lock...") to os.Stdout by default. Stdout carries
// the evaluated value, so a project whose kcl.mod declares a dependency kpm
// must fetch used to hand callers `cloning '...'` ahead of the JSON.

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

func TestRunKeepsKpmProgressOffStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL and runs git; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	pkgHome := t.TempDir()
	t.Setenv("KCL_PKG_PATH", pkgHome)

	// A local git repo stands in for a registry, so kpm has something to
	// fetch and the test needs no network.
	dep := t.TempDir()
	write := func(root, rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(dep, "kcl.mod", "[package]\nname = \"fakedep\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write(dep, "main.k", "greeting = \"hi\"\n")
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "i"},
		{"tag", "v0.0.1"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dep
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Another process holds the package-cache lock for the whole run. forge
	// never calls kpm's AcquirePackageCacheLock, so this does not itself print
	// "waiting for package-cache lock..."; it pins that contention cannot
	// change what stdout carries. The fail-before signal is the clone line.
	lockPath := filepath.Join(pkgHome, ".kpm", "config", "package-cache")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	held := flock.New(lockPath)
	if ok, err := held.TryLock(); err != nil || !ok {
		t.Fatalf("hold package-cache lock: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = held.Unlock() })

	proj := t.TempDir()
	write(proj, "deploy/kcl/kcl.mod", "[package]\nname = \"p\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\nfakedep = { git = \"file://"+filepath.ToSlash(dep)+"\", tag = \"v0.0.1\" }\n")
	write(proj, "deploy/kcl/dev/main.k", "import fakedep\nx = fakedep.greeting\n")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	out, runErr := kclrender.Run(proj, filepath.Join(proj, "deploy", "kcl", "dev"), []string{"env=dev"})
	os.Stdout = realStdout
	w.Close()
	leaked, _ := io.ReadAll(r)

	if runErr != nil {
		t.Fatalf("render: %v", runErr)
	}
	if !strings.Contains(string(out), `"hi"`) {
		t.Fatalf("dependency was not resolved: %s", out)
	}
	if len(leaked) != 0 {
		t.Errorf("kpm progress leaked onto stdout: %q", leaked)
	}
}
