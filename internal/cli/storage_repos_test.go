package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProjectReposFindsBuildContextAndReplaceSiblings(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for _, name := range []string{"app", "sibling", "replaced"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	app := filepath.Join(root, "app")
	if err := os.WriteFile(filepath.Join(app, "go.mod"), []byte("module x\n\ngo 1.22\n\nreplace example.com/r => ../replaced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "forge.yaml"), []byte("name: app\nmodule_path: example.com/app\ndocker:\n  build_contexts:\n    sib: ../sibling\n    img: docker-image://alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range projectRepos(app) {
		resolved, _ := filepath.EvalSymlinks(r)
		got[resolved] = true
	}
	for _, name := range []string{"app", "sibling", "replaced"} {
		want, _ := filepath.EvalSymlinks(filepath.Join(root, name))
		if !got[want] {
			t.Errorf("repo %s missing from %v", name, got)
		}
	}
}
