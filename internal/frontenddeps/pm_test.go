package frontenddeps

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The lockfile decides; with none, package.json's packageManager does (a fresh
// scaffold has no lockfile, and its first one must be written by the manager
// its manifest names); with neither, the declaration; then npm.
func TestPackageManagerPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		files    map[string]string
		declared string
		want     string
	}{
		{"nothing", nil, "", "npm"},
		{"npm migration with competing locks", map[string]string{"package.json": `{"packageManager":"npm@11.0.0"}`, "package-lock.json": "{}", "pnpm-lock.yaml": "", "yarn.lock": ""}, "", "npm"},
		{"yarn migration with competing locks", map[string]string{"package.json": `{"packageManager":"yarn@1.22.0"}`, "package-lock.json": "{}", "pnpm-lock.yaml": "", "yarn.lock": ""}, "", "yarn"},
		{"pnpm migration with competing locks", map[string]string{"package.json": `{"packageManager":"pnpm@12.15.0"}`, "package-lock.json": "{}", "pnpm-lock.yaml": "", "yarn.lock": ""}, "npm", "pnpm"},
		{"declared only", nil, "pnpm", "pnpm"},
		{"packageManager beats the declaration", map[string]string{"package.json": `{"packageManager":"pnpm@12.15.0+sha512.abc"}`}, "npm", "pnpm"},
		{"a lockfile beats packageManager", map[string]string{"package.json": `{"packageManager":"pnpm@12.15.0"}`, "package-lock.json": "{}"}, "", "npm"},
		{"pnpm lockfile", map[string]string{"pnpm-lock.yaml": ""}, "npm", "pnpm"},
		{"an unknown packageManager is ignored", map[string]string{"package.json": `{"packageManager":"bun@1.2.0"}`}, "", "npm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				writeFileAt(t, dir, rel, body)
			}
			if got := PackageManager(dir, tc.declared); got != tc.want {
				t.Errorf("PackageManager = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallArgvFollowsTheManager(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, dir, "package.json", `{}`)
	writeFileAt(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	if got := InstallCommand(dir, "", true); got != "pnpm install --frozen-lockfile" {
		t.Errorf("frozen pnpm install = %q", got)
	}
	if got := InstallStamp(dir, "pnpm"); got != filepath.Join(dir, "node_modules", ".modules.yaml") {
		t.Errorf("pnpm stamp = %q", got)
	}
}

// A pnpm project still installs where only node is on PATH.
func TestResolveRunnerFallsBackWhenTheManagerIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, dir, "package.json", `{"packageManager":"pnpm@12.15.0"}`)
	have := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	for _, tc := range []struct {
		name string
		path func(string) (string, error)
		want string
	}{
		{"on PATH", have("pnpm", "corepack", "npm"), "pnpm"},
		{"corepack", have("corepack", "npm"), "corepack pnpm@12.15.0"},
		{"npm exec", have("npm"), "npm exec --yes --package=pnpm@12.15.0 -- pnpm"},
	} {
		if got := strings.Join(resolveRunner(dir, "pnpm", tc.path), " "); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := strings.Join(resolveRunner(dir, "npm", have()), " "); got != "npm" {
		t.Errorf("npm is never wrapped: %q", got)
	}
}

func TestCopiedFromStoreWarning(t *testing.T) {
	if w := copiedFromStoreWarning("Packages are hard linked from the content-addressable store to the virtual store."); w != "" {
		t.Errorf("a linking install warned: %s", w)
	}
	w := copiedFromStoreWarning("Packages are copied from the content-addressable store to the virtual store.\n  Content-addressable store is at: /cache/pnpm-store/v11")
	if !strings.Contains(w, "different mount") {
		t.Errorf("a copying install did not explain the mount: %q", w)
	}
}

// pnpm 12 writes a two-document lockfile when package.json pins pnpm: the
// first document holds only the pinned pnpm, the second the project.
func TestLockDriftPNPMMultiDocument(t *testing.T) {
	root := t.TempDir()
	writeFileAt(t, root, "package.json", manifest("^0.9.0"))
	lock := Lock{Dir: root, File: "pnpm-lock.yaml", Runner: "pnpm"}
	envDoc := "---\nlockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    configDependencies: {}\n    packageManagerDependencies:\n      pnpm:\n        specifier: 12.15.0\n        version: 12.15.0\n\npackages:\n\n  pnpm@12.15.0:\n    resolution: {integrity: sha512-x}\n\n"
	project := func(spec string) string {
		return "---\nlockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    dependencies:\n      '" + runtimePkg +
			"':\n        specifier: " + spec + "\n        version: 0.9.1\n      react:\n        specifier: 19.0.0\n        version: 19.0.0\n"
	}
	writeFileAt(t, root, "pnpm-lock.yaml", envDoc+project("^0.9.0"))
	if d, err := LockDrift(lock, root); err != nil || d != nil {
		t.Fatalf("agreeing two-document lock reported drift: %+v %v", d, err)
	}
	writeFileAt(t, root, "pnpm-lock.yaml", envDoc+project("0.8.0"))
	if d, err := LockDrift(lock, root); err != nil || d == nil || len(d.Details) != 1 {
		t.Fatalf("stale two-document lock: %+v %v", d, err)
	}
}
