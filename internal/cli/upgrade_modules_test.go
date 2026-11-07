package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

func upgradeWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A file:// module proxy exercises real Go resolution without any network or
// replacements that would make the version-compatibility check pass trivially.
func upgradeModuleProxy(t *testing.T) string {
	t.Helper()
	proxy := t.TempDir()
	dir := filepath.Join(proxy, forgecompat.ModulePath, "@v")
	for _, version := range []string{"v0.1.0-issue762.fixture1", "v0.1.1-issue762.fixture1"} {
		mod := "module " + forgecompat.ModulePath + "\n\ngo 1.24\n"
		upgradeWrite(t, filepath.Join(dir, version+".mod"), mod)
		upgradeWrite(t, filepath.Join(dir, version+".info"), fmt.Sprintf(`{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, version))
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range map[string]string{"go.mod": mod, "forge.go": "package forge\n"} {
			w, err := zw.Create(forgecompat.ModulePath + "@" + version + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, version+".zip"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upgradeWrite(t, filepath.Join(dir, "list"), "v0.1.0-issue762.fixture1\nv0.1.1-issue762.fixture1\n")
	return "file://" + filepath.ToSlash(proxy)
}

func TestUpgradeModulesOfflineCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("real Go subprocesses and module resolution; runs in task test")
	}
	t.Setenv("GOPROXY", upgradeModuleProxy(t))
	// The fixture proxy must be the ONLY source. A developer machine with
	// GOPRIVATE=github.com/reliant-labs/* (in the environment or in `go env -w`)
	// otherwise sends forge's module straight to git, where the fixture
	// versions do not exist. GOENV=off drops the go env file; the empty values
	// clear the process environment.
	t.Setenv("GOENV", "off")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GONOSUMDB", "")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")
	buildinfo.Set("v0.1.1-issue762.fixture1", "", "unknown")
	t.Cleanup(func() { buildinfo.Set("dev", "", "unknown") })
	root := newTestProjectWithVersion(t, "v0.1.0-issue762.fixture1")
	for _, dir := range []string{".", "gen", "tools/worker"} {
		upgradeWrite(t, filepath.Join(root, dir, "go.mod"), "module example.com/app"+strings.ReplaceAll(strings.TrimPrefix(dir, "."), "/", "_")+"\n\ngo 1.24\n\nrequire "+forgecompat.ModulePath+" v0.1.0-issue762.fixture1\n")
		upgradeWrite(t, filepath.Join(root, dir, "main.go"), "package app\nimport _ \""+forgecompat.ModulePath+"\"\n")
	}
	// Seed the old graph so the preflight can resolve it without missing sums.
	for _, dir := range []string{".", "gen", "tools/worker"} {
		if out, err := runGoOffWorkspace(filepath.Join(root, dir), "mod", "tidy"); err != nil {
			t.Fatalf("seed old module: %v\n%s", err, out)
		}
	}
	// Prove the actual generate preflight refuses this fixture BEFORE upgrading.
	if err := checkPkgCompat(root); err == nil {
		t.Fatal("old pin unexpectedly passes generate compatibility")
	}
	withCwd(t, root, func() {
		_ = captureStdout(t, func() {
			if err := runUpgrade(false, false, nil, "v0.1.1-issue762.fixture1"); err != nil {
				t.Fatal(err)
			}
		})
	})
	for _, dir := range []string{".", "gen", "tools/worker"} {
		mod, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(mod), forgecompat.ModulePath+" v0.1.1-issue762.fixture1") {
			t.Errorf("%s/go.mod did not upgrade:\n%s", dir, mod)
		}
		assessment, ok := forgecompat.Assess(filepath.Join(root, dir))
		if !ok || assessment.ProjectVersion != "v0.1.1-issue762.fixture1" || assessment.Verdict != forgecompat.OK {
			t.Errorf("module must actually resolve target, not silently skip compatibility: %+v, resolved=%v", assessment, ok)
		}
		if err := checkPkgCompat(filepath.Join(root, dir)); err != nil {
			t.Errorf("generate compatibility after upgrade (%s): %v", dir, err)
		}
	}
}

// Fake runner tests stay fast/cache-sound and force failures AFTER writes,
// including a later module failing after an earlier module succeeded.
func TestUpgradeModuleFailureRestoresProject(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			root := newTestProjectWithVersion(t, "v0.1.0")
			original := map[string]string{
				"go.mod":     "module example.com/app\nrequire " + forgecompat.ModulePath + " v0.1.0\n",
				"go.sum":     "user's existing sums\n",
				"gen/go.mod": "module example.com/app/gen\nrequire " + forgecompat.ModulePath + " v0.1.0\n",
				"Dockerfile": "FROM user's-edits\n",
				"go.work":    "go 1.24\nuse (. ./gen ../external)\n",
			}
			yaml, err := os.ReadFile(filepath.Join(root, "forge.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			original["forge.yaml"] = string(yaml)
			for path, body := range original {
				upgradeWrite(t, filepath.Join(root, path), body)
			}
			calls := 0
			run := func(dir string, args ...string) ([]byte, error) {
				calls++
				if args[0] == "get" {
					fakeUpgradeRequire(t, dir, "v0.1.1")
				}
				upgradeWrite(t, filepath.Join(dir, "go.sum"), "partial go writes\n")
				if calls == failAt {
					return []byte("fixture resolution failed"), errors.New("offline")
				}
				return nil, nil
			}
			var runErr error
			var out string
			withCwd(t, root, func() {
				out = captureStdout(t, func() { runErr = runUpgradeWithGo(false, true, false, nil, "v0.1.1", run) })
			})
			if runErr == nil {
				t.Fatal("failed Go command reported success")
			}
			for _, want := range []string{"upgrade incomplete", "fixture resolution failed", "Module files restored", "Next:", "(cd '.' && GOWORK=off go get '" + forgecompat.ModulePath + "@v0.1.1' && GOWORK=off go mod tidy)", "(cd 'gen'"} {
				if !strings.Contains(runErr.Error(), want) {
					t.Errorf("missing %q: %v", want, runErr)
				}
			}
			if strings.Contains(out, "forge.yaml updated") || strings.Contains(out, "requirements synchronized") {
				t.Errorf("false success: %s", out)
			}
			for path, body := range original {
				b, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				if string(b) != body {
					t.Errorf("failure changed %s: %s", path, b)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "gen/go.sum")); !os.IsNotExist(err) {
				t.Errorf("new gen/go.sum survived rollback: %v", err)
			}
		})
	}
}

func fakeUpgradeRequire(t *testing.T, dir, target string) {
	t.Helper()
	path := filepath.Join(dir, "go.mod")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.Parse(path, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AddRequire(forgecompat.ModulePath, target); err != nil {
		t.Fatal(err)
	}
	b, err = f.Format()
	if err != nil {
		t.Fatal(err)
	}
	upgradeWrite(t, path, string(b))
}

func TestUpgradeModulesDiscoveryOrderAndPreservesBridges(t *testing.T) {
	root := t.TempDir()
	old := "module example.com/app\nrequire " + forgecompat.ModulePath + " v0.1.0\nreplace " + forgecompat.ModulePath + " => ../forge\n"
	for _, dir := range []string{".", "gen", "other/worker"} {
		upgradeWrite(t, filepath.Join(root, dir, "go.mod"), old)
	}
	for _, dir := range []string{"vendor/dependency", "node_modules/dep", ".cache/dep", "testdata/fixture"} {
		upgradeWrite(t, filepath.Join(root, dir, "go.mod"), old)
	}
	upgradeWrite(t, filepath.Join(root, "unrelated/go.mod"), "module example.com/unrelated\n")
	work := "go 1.24\nuse (\n.\n./gen\n../forge\n)\n"
	upgradeWrite(t, filepath.Join(root, "go.work"), work)
	var calls []string
	run := func(dir string, args ...string) ([]byte, error) {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, filepath.ToSlash(rel)+":"+strings.Join(args, " "))
		if args[0] == "get" {
			fakeUpgradeRequire(t, dir, "v0.1.1")
		}
		return nil, nil
	}
	if err := upgradeForgeModules(root, "0.1.1", false, run); err != nil {
		t.Fatal(err)
	}
	want := []string{
		".:get " + forgecompat.ModulePath + "@v0.1.1", "gen:get " + forgecompat.ModulePath + "@v0.1.1", "other/worker:get " + forgecompat.ModulePath + "@v0.1.1",
		".:mod tidy", "gen:mod tidy", "other/worker:mod tidy",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("commands = %v, want %v", calls, want)
	}
	for _, dir := range []string{".", "gen", "other/worker"} {
		b, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "replace "+forgecompat.ModulePath+" => ../forge") {
			t.Errorf("lost replace: %s", b)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != work {
		t.Fatal("rewrote workspace")
	}
	calls = nil
	if err := upgradeForgeModules(root, "v0.1.1", false, run); err != nil {
		t.Fatal(err)
	}
	if len(calls) > 0 {
		t.Errorf("current pins spawned Go: %v", calls)
	}
}

func TestUpgradeModulesLocalReplaceAndWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("real Go get/tidy subprocesses; runs in task test")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	checkout := t.TempDir()
	upgradeWrite(t, filepath.Join(checkout, "go.mod"), "module "+forgecompat.ModulePath+"\ngo 1.24\n")
	upgradeWrite(t, filepath.Join(checkout, "forge.go"), "package forge\n")
	// A workspace referencing unavailable external modules must not leak into
	// get/tidy or cause edits outside the project. Replacements survive unchanged.
	work := "go 1.24\nuse (\n.\n./gen\n../external-missing\n)\n"
	upgradeWrite(t, filepath.Join(root, "go.work"), work)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	for _, dir := range []string{".", "gen"} {
		upgradeWrite(t, filepath.Join(root, dir, "go.mod"), "module example.com/app"+strings.TrimPrefix(dir, ".")+"\ngo 1.24\nrequire "+forgecompat.ModulePath+" v0.1.0\nreplace "+forgecompat.ModulePath+" => "+filepath.ToSlash(checkout)+"\n")
		upgradeWrite(t, filepath.Join(root, dir, "main.go"), "package app\nimport _ \""+forgecompat.ModulePath+"\"\n")
	}
	if err := upgradeForgeModules(root, "v0.1.1", false, runGoOffWorkspace); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".", "gen"} {
		b, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "require "+forgecompat.ModulePath+" v0.1.1") || !strings.Contains(string(b), "=> "+filepath.ToSlash(checkout)) {
			t.Errorf("lost pin/replace: %s", b)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != work {
		t.Fatal("rewrote workspace")
	}
}

func TestUpgradeModulesRejectsFalseGoSuccess(t *testing.T) {
	root := t.TempDir()
	old := "module example.com/app\nrequire " + forgecompat.ModulePath + " v0.1.0\n"
	upgradeWrite(t, filepath.Join(root, "go.mod"), old)
	if err := upgradeForgeModules(root, "v0.1.1", false, func(string, ...string) ([]byte, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "still requires") {
		t.Fatalf("wrong final pin reported success: %v", err)
	}
}

func TestUpgradeModulesPseudoVersionRecovery(t *testing.T) {
	next := upgradeModulesNext([]string{".", "path with 'quote'"}, "v0.1.1-0.20260101000000-abcdef123456")
	for _, want := range []string{"go work use", "go work init .", "pushed", shellQuote("path with 'quote'")} {
		if !strings.Contains(next, want) {
			t.Errorf("missing %q: %s", want, next)
		}
	}
}

func TestUpgradeCheckReportsModuleDriftReadOnly(t *testing.T) {
	root := newTestProjectWithVersion(t, "v0.1.1-issue762.fixture1") // YAML already current: modules still need repair.
	old := "module example.com/app\n\ngo 1.24\n\nrequire " + forgecompat.ModulePath + " v0.1.0-issue762.fixture1\n"
	for _, dir := range []string{".", "gen", "tools/worker"} {
		upgradeWrite(t, filepath.Join(root, dir, "go.mod"), old)
	}
	var out string
	withCwd(t, root, func() {
		out = captureStdout(t, func() {
			if err := runUpgradeWithGo(true, false, false, nil, "v0.1.1-issue762.fixture1", func(string, ...string) ([]byte, error) { t.Fatal("--check spawned Go"); return nil, nil }); err != nil {
				t.Fatal(err)
			}
		})
	})
	for _, dir := range []string{".", "gen", "tools/worker"} {
		rel := filepath.ToSlash(filepath.Join(dir, "go.mod"))
		if !strings.Contains(out, rel+": v0.1.0-issue762.fixture1 → v0.1.1-issue762.fixture1") {
			t.Errorf("missing module drift for %s:\n%s", rel, out)
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != old {
			t.Errorf("--check wrote %s", rel)
		}
	}
}
