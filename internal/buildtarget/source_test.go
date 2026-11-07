package buildtarget

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo creates a git repository at dir with one commit holding files, and
// returns its HEAD. Identity is set in the repository, never globally.
func gitRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "forge-test@example.com"},
		{"config", "user.name", "forge test"},
		{"add", "."},
		{"commit", "--quiet", "-m", "fixture"},
	} {
		run(t, dir, args...)
	}
	return run(t, dir, "rev-parse", "HEAD")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCaptureSource_NotACheckout(t *testing.T) {
	src, err := CaptureSource(context.Background(), t.TempDir(), t.TempDir())
	if err != nil || src != nil {
		t.Fatalf("a plain directory has no commit to record: got %+v, %v", src, err)
	}
}

// A sibling checkout: its own repository, origin canonicalized, the module
// its nearest go.mod declares, and clean until something is left uncommitted.
func TestCaptureSource_SiblingCheckout(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "acme")
	gitRepo(t, project, map[string]string{"go.mod": "module github.com/example/acme\n"})
	sib := filepath.Join(root, "sib")
	head := gitRepo(t, sib, map[string]string{
		"go.mod":         "module example.com/sib\n",
		"tool/go.mod":    "module example.com/sib/tool\n",
		"tool/README.md": "x\n",
	})
	run(t, sib, "remote", "add", "origin", "https://x-access-token:secret@github.com/Example/sib.git")

	src, err := CaptureSource(context.Background(), filepath.Join(sib, "tool"), project)
	if err != nil || src == nil {
		t.Fatalf("CaptureSource: %+v, %v", src, err)
	}
	// Dir is the OS-native, symlink-resolved path — the spelling every other
	// path forge holds uses — not git's forward-slash `C:/Users/…` form.
	real, _ := filepath.EvalSymlinks(sib)
	// No scheme, no credential, no ".git"; the path keeps its case.
	want := Source{Dir: real, Repo: "github.com/Example/sib", Module: "example.com/sib/tool", ModuleDir: "tool", Commit: head}
	if *src != want {
		t.Errorf("source = %+v\nwant     %+v", *src, want)
	}
	if !src.External() {
		t.Error("a sibling repository must be External")
	}
	if strings.Contains(src.Repo, "secret") {
		t.Errorf("a credential in the remote URL was recorded: %q", src.Repo)
	}

	// An untracked, unignored file is a change no commit records.
	if err := os.WriteFile(filepath.Join(sib, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err = CaptureSource(context.Background(), sib, project)
	if err != nil || src == nil || !src.Dirty {
		t.Fatalf("untracked file must read as dirty: %+v, %v", src, err)
	}
	if src.Module != "example.com/sib" || src.ModuleDir != "." {
		t.Errorf("module at the top level = (%q, %q), want (example.com/sib, .)", src.Module, src.ModuleDir)
	}
	if rel := src.ForRelease(); rel == nil || rel.Commit != head || !rel.Dirty || rel.Repo != src.Repo {
		t.Errorf("ForRelease = %+v, want repo/commit/dirty and no path", rel)
	}
}

// A ShellBuild in the project's own checkout is the project's commit, which
// release provenance already owns — it is not an external source.
func TestCaptureSource_ProjectCheckout(t *testing.T) {
	project := t.TempDir()
	gitRepo(t, project, map[string]string{"go.mod": "module github.com/example/acme\n", "tools/x.txt": "x\n"})
	src, err := CaptureSource(context.Background(), filepath.Join(project, "tools"), project)
	if err != nil || src == nil {
		t.Fatalf("CaptureSource: %+v, %v", src, err)
	}
	if !src.Project || src.External() {
		t.Errorf("the project's own checkout must be Project, not External: %+v", src)
	}
}

func TestSourceResolveCommit(t *testing.T) {
	dir := t.TempDir()
	head := gitRepo(t, dir, map[string]string{"a": "a\n"})
	run(t, dir, "tag", "v1.2.3")
	src := &Source{Dir: dir}
	got, err := src.ResolveCommit(context.Background(), "refs/tags/v1.2.3")
	if err != nil || got != head {
		t.Errorf("ResolveCommit(v1.2.3) = %q, %v; want %s", got, err, head)
	}
	if _, err := src.ResolveCommit(context.Background(), "refs/tags/v9.9.9"); err == nil {
		t.Error("a tag the checkout does not have must not resolve")
	}
}
