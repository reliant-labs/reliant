package kclrender_test

// workdir_eval_test.go pins RunInWorkDir: the opt-in form under which a
// project's KCL reads FILES relative to workDir rather than to the caller's
// process cwd.
//
// kpm's client.WithWorkDir decides where kpm resolves the PACKAGE from. It does
// NOT reach the KCL runtime's `file.read`, which resolves a relative path
// against the real process cwd. So a project whose KCL reads a file by
// project-relative path — control-plane's deploy/kcl/lib/barman_plugin.k does
// `file.read("deploy/cnpg/plugin-barman-cloud.yaml")` — rendered correctly when
// forge was invoked from the project root and failed with "No such file or
// directory" from anywhere else, naming a path that plainly exists. Every shell
// script reading one of those files cd'd there first, which is how the gap
// stayed hidden.
//
// The last test here pins the opt-in half, which matters as much as the fix:
// plain Run must NOT chdir, because os.Chdir is process-global and this is a
// library. Making it unconditional turned
// internal/templates.TestBornContractTestSurvivesDepValidation intermittently
// red — it resolves the forge module root as filepath.Abs("../..") inside a
// t.Parallel() subtest, and picked up a render's workDir instead.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// TestRunResolvesFileReadAgainstWorkDirNotTheCallerCwd renders the same
// project from two different process cwds and requires the same answer.
func TestRunInWorkDirResolvesFileReadAgainstWorkDir(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"deploy/kcl/kcl.mod":      "[package]\nname = \"workdir_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/dev/main.k":   "import forge\nimport file\n\npinned = file.read(\"deploy/vendored/pin.txt\")\n",
		"deploy/vendored/pin.txt": "v0.15.0",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	envDir := filepath.Join(dir, "deploy", "kcl", "dev")

	// An UNRELATED directory, which is what a caller using -C stands in.
	// Deliberately not a subdirectory of the project: the failing case was
	// any cwd that is not the project root.
	for _, cwd := range []string{dir, t.TempDir(), filepath.Join(dir, "deploy", "kcl")} {
		t.Chdir(cwd)
		out, err := kclrender.RunInWorkDir(dir, envDir, []string{"env=dev"})
		if err != nil {
			t.Fatalf("render from cwd %s: a project-relative file.read must resolve against workDir: %v", cwd, err)
		}
		if !strings.Contains(string(out), "v0.15.0") {
			t.Errorf("render from cwd %s did not read the file:\n%s", cwd, out)
		}
	}
}

// TestRunRestoresTheCallerCwd: the render's chdir is an implementation detail
// and must not leak. A command that renders and then resolves a relative path
// of its own — or a test that renders and then reads a fixture — would
// otherwise silently read from the project instead.
func TestRunInWorkDirRestoresTheCallerCwd(t *testing.T) {
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"deploy/kcl/kcl.mod":    "[package]\nname = \"cwd_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/dev/main.k": "import forge\n\nok = forge.Bundle is not None\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	caller := t.TempDir()
	t.Chdir(caller)
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kclrender.RunInWorkDir(dir, filepath.Join(dir, "deploy", "kcl", "dev"), []string{"env=dev"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("render left the process in %s; it must restore %s", after, before)
	}

	// And a render that FAILS must restore it too — the defer, not the happy
	// path. A refused render is the common case in a validation loop, so a
	// leak here would be the one that actually bit.
	_, err = kclrender.RunInWorkDir(dir, filepath.Join(dir, "deploy", "kcl", "nope"), nil)
	if err == nil {
		t.Fatal("render of a nonexistent env succeeded")
	}
	if got, _ := os.Getwd(); got != before {
		t.Errorf("a FAILED render left the process in %s; it must restore %s", got, before)
	}
}

// TestRunResolvesARelativeProjectDirOnce: a caller holding a RELATIVE project
// dir builds its source the ordinary Go way, filepath.Join(projectDir, ...),
// so both arguments are relative to the process cwd. kpm resolves a relative
// source against workDir instead, which named every path twice:
//
//	kpm run shop/deploy/kcl/dev/zz_forge_frontend_config_probe_17401.k:
//	Cannot find the kcl file, please check the file path
//	shop/shop/deploy/kcl/dev/zz_forge_frontend_config_probe_17401.k
//
// That is what `forge project new shop` printed on every scaffold — its
// target is `<--path>/<name>`, relative — and the fallback it took wrote the
// dev frontend's config.js from proto defaults, pointing the browser at the
// dev IdP's port instead of the API's. Both entry points, and a single-file
// source as well as a package dir.
func TestRunResolvesARelativeProjectDirOnce(t *testing.T) {
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	parent := t.TempDir()
	for rel, body := range map[string]string{
		"shop/deploy/kcl/kcl.mod":     "[package]\nname = \"relative_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"shop/deploy/kcl/dev/main.k":  "import forge\n\nok = forge.Bundle is not None\n",
		"shop/deploy/kcl/dev/probe.k": "probe = \"single file\"\n",
	} {
		p := filepath.Join(parent, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(parent)

	projectDir := "shop"
	for name, run := range map[string]func(string, string, []string) ([]byte, error){
		"Run": kclrender.Run, "RunInWorkDir": kclrender.RunInWorkDir,
	} {
		for _, source := range []string{
			filepath.Join(projectDir, "deploy", "kcl", "dev"),
			filepath.Join(projectDir, "deploy", "kcl", "dev", "probe.k"),
		} {
			if _, err := run(projectDir, source, []string{"env=dev"}); err != nil {
				t.Errorf("%s(%q, %q) from %s: a cwd-relative source must resolve once: %v", name, projectDir, source, parent, err)
			}
		}
	}
}

// TestRunDoesNotChdir pins the OPT-IN half. os.Chdir is process-global, so a
// library that moved the process on every render would change the meaning of
// every relative path in the calling program for the duration — including in
// unrelated code that never asked for a render.
//
// That is a measured failure, not a theoretical one: making the chdir
// unconditional turned internal/templates.TestBornContractTestSurvivesDepValidation
// intermittently red, because it computes the forge module root as
// filepath.Abs(filepath.Join("..", "..")) inside a t.Parallel() subtest and
// resolved it against a concurrent render's workDir. A flaky suite is a worse
// defect than the `-C` bug being fixed, so the shared render paths stay
// byte-identical and only a caller that knows it is alone opts in.
func TestRunDoesNotChdir(t *testing.T) {
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"deploy/kcl/kcl.mod":    "[package]\nname = \"nochdir_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/dev/main.k": "import forge\n\nok = forge.Bundle is not None\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	caller := t.TempDir()
	t.Chdir(caller)
	// A relative path the CALLER owns. If Run moved the process, this would
	// resolve somewhere inside the rendered project instead.
	if err := os.WriteFile("marker.txt", []byte("caller"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := kclrender.Run(dir, filepath.Join(dir, "deploy", "kcl", "dev"), []string{"env=dev"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("marker.txt")
	if err != nil || string(got) != "caller" {
		t.Errorf("after Run, the caller's relative path no longer resolves to the caller's dir (%q, %v); "+
			"plain Run must not chdir", got, err)
	}
}
