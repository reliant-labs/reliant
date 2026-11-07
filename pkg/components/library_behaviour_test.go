package components

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestComponentBehaviour runs the vitest suites in testdata/behavior/ against
// the library components they import, in jsdom with real React event
// delegation: the event-ordering behaviour (portals, capture handlers, native
// document listeners) that a source-string guard cannot prove. Each suite is
// laid out next to its components exactly as `forge component install` does.
func TestComponentBehaviour(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: installs react, vitest and jsdom with npm; runs in the full tier")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("npm (node) not on PATH under CI: install node so the component behaviour suites run")
		}
		t.Skip("npm not on PATH")
	}

	suites, err := filepath.Glob(filepath.Join("testdata", "behavior", "*.test.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) == 0 {
		t.Fatal("no suites under testdata/behavior — this guard has gone blind")
	}

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	lib := NewLibrary()
	components := map[string]bool{}
	for _, suite := range suites {
		body, err := os.ReadFile(suite)
		if err != nil {
			t.Fatal(err)
		}
		write("src/"+filepath.Base(suite), string(body))
		for _, m := range siblingImport.FindAllStringSubmatch(string(body), -1) {
			if _, ok := byName[m[1]]; !ok {
				t.Fatalf("%s imports ./%s, which is not a library component", suite, m[1])
			}
			components[m[1]] = true
			deps, err := lib.Dependencies(m[1])
			if err != nil {
				t.Fatal(err)
			}
			for _, dep := range deps {
				components[dep] = true
			}
		}
	}
	for name := range components {
		body, err := lib.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		write("src/"+name+".tsx", body)
	}

	write("package.json", `{"private":true,"type":"module","devDependencies":{"react":"^19.1.0","react-dom":"^19.1.0","vitest":"^4.1.11","jsdom":"^27.0.0"}}`)
	write("vitest.config.mjs", `export default { esbuild: { jsx: "automatic" }, test: { environment: "jsdom", include: ["src/**/*.test.tsx"] } };`)

	// --legacy-peer-deps: npm 10 crashes resolving vitest's optional jsdom
	// peer and its optional canvas peer ("reading 'edgesOut'").
	install := exec.Command(npm, "install", "--no-audit", "--no-fund", "--ignore-scripts", "--loglevel=error", "--legacy-peer-deps")
	install.Dir = dir
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("npm install: %v\n%s", err, out)
	}
	vitest := exec.Command(filepath.Join(dir, "node_modules", ".bin", "vitest"), "run")
	vitest.Dir = dir
	vitest.Env = append(os.Environ(), "CI=1")
	if out, err := vitest.CombinedOutput(); err != nil {
		names := make([]string, 0, len(suites))
		for _, s := range suites {
			names = append(names, filepath.Base(s))
		}
		t.Fatalf("a component behaviour suite failed (%s):\n%s", strings.Join(names, ", "), out)
	}
}
