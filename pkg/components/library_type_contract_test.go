package components

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestComponentTypeContracts runs tsc over every fixture in
// testdata/typecheck/ with the scaffolded tsconfig's strictness. A fixture's
// `@ts-expect-error` lines are misuses a component's type must reject; a
// loosened type turns each into an "unused directive" error. Each fixture is
// compiled against the library components it imports (`./x`) and their own
// sibling imports, exactly as `forge component install` lays them out.
func TestComponentTypeContracts(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: installs typescript and @types/react with npm; runs in the full tier")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("npm (node) not on PATH under CI: install node so the component type contracts run")
		}
		t.Skip("npm not on PATH")
	}

	fixtures, err := filepath.Glob(filepath.Join("testdata", "typecheck", "*.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures under testdata/typecheck — this guard has gone blind")
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
	for _, fixture := range fixtures {
		body, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		write("src/"+filepath.Base(fixture), string(body))
		for _, m := range siblingImport.FindAllStringSubmatch(string(body), -1) {
			if _, ok := byName[m[1]]; !ok {
				t.Fatalf("%s imports ./%s, which is not a library component", fixture, m[1])
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
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := lib.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		write("src/"+name+".tsx", body)
	}

	// The compiler options the scaffolded Next.js tsconfig sets that bear on
	// a component (internal/templates/frontend/nextjs/tsconfig.json.tmpl).
	write("tsconfig.json", `{"compilerOptions":{"target":"ES2022","lib":["dom","dom.iterable","esnext"],
"skipLibCheck":true,"strict":true,"noEmit":true,"esModuleInterop":true,"module":"esnext",
"moduleResolution":"bundler","isolatedModules":true,"noUncheckedIndexedAccess":true,"jsx":"react-jsx"},
"include":["src/**/*.tsx"]}`)
	// The scaffold's own ranges (nextjs/package.json.tmpl).
	write("package.json", `{"private":true,"devDependencies":{"typescript":"^5.8.0","@types/react":"^19.1.0","@types/react-dom":"^19.1.0"}}`)

	install := exec.Command(npm, "install", "--no-audit", "--no-fund", "--ignore-scripts", "--loglevel=error")
	install.Dir = dir
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("npm install: %v\n%s", err, out)
	}
	tsc := exec.Command(filepath.Join(dir, "node_modules", ".bin", "tsc"), "-p", ".")
	tsc.Dir = dir
	if out, err := tsc.CombinedOutput(); err != nil {
		t.Fatalf("a component type contract failed (components: %s):\n%s", strings.Join(names, ", "), out)
	}
}
