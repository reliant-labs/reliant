package kcleval_test

// fileread_eval_test.go pins WHICH directory a file's `file.read` resolves
// against, which is the one thing the evaluation's working directory decides.
//
// It is a separate file because getting this wrong is silent. kpm resolves a
// relative `lib.*` import from the file's OWN kcl.mod package, whatever the
// cwd is — so an evaluation that chose the package root as its cwd would
// still import correctly and would still LOOK right. What it would break is
// `file.read`, which takes a path relative to the process cwd: control-plane's
// lib/barman_plugin.k reads "deploy/cnpg/plugin-barman-cloud.yaml", a
// PROJECT-ROOT-relative path, and resolving it from deploy/kcl would fail on
// a missing file — or, worse, read a different file that happened to exist
// there.
//
// So the working directory is the project root, matching `env render` exactly.
// One evaluation semantics for the whole project, not one per file location.

import (
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kcleval"
)

// TestEvalReadsFilesRelativeToTheProjectRoot: a `file.read` of a
// project-root-relative path resolves, and it resolves from a subdirectory cwd
// too — the caller's location changes nothing.
func TestEvalReadsFilesRelativeToTheProjectRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod": kclMod,
		// The shape control-plane's lib/barman_plugin.k has: a nested library
		// file reading a path anchored at the PROJECT root, not at its own
		// directory and not at its package root.
		"deploy/kcl/lib/vendored.k": "import forge\nimport file\n\npinned = file.read(\"deploy/cnpg/plugin.yaml\")\n_p = forge.Bundle is not None\n",
		"deploy/cnpg/plugin.yaml":   "image: plugin:v0.15.0\n",
	})

	want := "image: plugin:v0.15.0\n"
	for _, cwd := range []string{dir, filepath.Join(dir, "deploy", "kcl", "lib")} {
		t.Chdir(cwd)
		res, err := kcleval.Eval(kcleval.Request{
			ProjectDir: dir, File: "deploy/kcl/lib/vendored.k", Selectors: []string{"pinned"},
		})
		if err != nil {
			t.Fatalf("cwd %s: file.read of a project-root-relative path must resolve: %v", cwd, err)
		}
		if res.Value != want {
			t.Errorf("cwd %s: pinned = %#v, want %#v", cwd, res.Value, want)
		}
	}
}
