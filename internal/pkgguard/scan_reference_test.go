package pkgguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScan_FlagsEveryReferenceNotOnlyCalls pins what the guard catches. An
// environment read does not have to be a call at the read site: #555 put
// `getenv: os.Getenv` into a struct literal as a test seam's default and
// called it later through the field. forbidigo flagged that line in CI, and
// this guard — which only inspected call expressions — passed it, so the two
// halves of the rule disagreed about the same file.
//
// Each case is one file in a throwaway module, scanned with the REAL
// pkg/.golangci.yml policy, so the patterns and exemptions under test are the
// ones CI enforces.
func TestScan_FlagsEveryReferenceNotOnlyCalls(t *testing.T) {
	t.Parallel()
	policy, err := LoadForbidigoPolicy(filepath.Join(repoRoot(t), "pkg", ".golangci.yml"))
	if err != nil {
		t.Fatalf("load the library's lint policy: %v", err)
	}

	cases := []struct {
		name string
		path string // relative to the scanned root, as under pkg/
		src  string
		want []string // Finding.Ref values, in line order; nil = must be clean
	}{
		{
			name: "direct call",
			path: "fixture/call.go",
			src:  "package fixture\n\nimport \"os\"\n\nfunc f() string { return os.Getenv(\"X\") }\n",
			want: []string{"os.Getenv"},
		},
		{
			name: "function value in a struct literal (the #555 shape)",
			path: "fixture/value.go",
			src: "package fixture\n\nimport \"os\"\n\ntype policy struct{ getenv func(string) string }\n\n" +
				"func newPolicy() *policy { return &policy{getenv: os.Getenv} }\n",
			want: []string{"os.Getenv"},
		},
		{
			name: "package-level variable",
			path: "fixture/pkgvar.go",
			src:  "package fixture\n\nimport \"os\"\n\nvar lookup = os.LookupEnv\n",
			want: []string{"os.LookupEnv"},
		},
		{
			name: "local assignment",
			path: "fixture/local.go",
			src:  "package fixture\n\nimport \"os\"\n\nfunc f() int { all := os.Environ; return len(all()) }\n",
			want: []string{"os.Environ"},
		},
		{
			name: "aliased import",
			path: "fixture/alias.go",
			src:  "package fixture\n\nimport goos \"os\"\n\nfunc f() string { return goos.Getenv(\"X\") }\n",
			want: []string{"os.Getenv"},
		},
		{
			name: "dot import",
			path: "fixture/dot.go",
			src:  "package fixture\n\nimport . \"os\"\n\nfunc f() string { return Getenv(\"X\") }\n",
			want: []string{"os.Getenv"},
		},
		{
			name: "under a dot import, declared names are names, not references",
			path: "fixture/dotnames.go",
			src: "package fixture\n\nimport . \"os\"\n\nvar _ = Getpid\n\n" +
				"type deps struct{ Getenv func(string) string }\n\n" +
				"func (deps) Environ() []string { return nil }\n\n" +
				"func f() deps { return deps{Getenv: nil} }\n",
		},
		{
			name: "a local that shadows the package name is not the package",
			path: "fixture/shadow.go",
			src: "package fixture\n\nimport \"os\"\n\nvar _ = os.Getpid\n\n" +
				"type fake struct{}\n\nfunc (fake) Getenv(string) string { return \"\" }\n\n" +
				"func f() string { os := fake{}; return os.Getenv(\"X\") }\n",
		},
		{
			name: "a method of the same name on a value is not the os function",
			path: "fixture/method.go",
			src: "package fixture\n\ntype env struct{ Getenv func(string) string }\n\n" +
				"func f(e env) string { return e.Getenv(\"X\") }\n",
		},
		{
			name: "a field named like the function is not a reference",
			path: "fixture/field.go",
			src:  "package fixture\n\ntype deps struct {\n\tGetenv func(string) string\n}\n",
		},
		{
			name: "the sanctioned config loader is exempt",
			path: "config/loader.go",
			src:  "package config\n\nimport \"os\"\n\nvar read = os.Getenv\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			file := filepath.Join(root, filepath.FromSlash(c.path))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(c.src), 0o644); err != nil {
				t.Fatal(err)
			}

			findings, files, err := Scan(root, policy)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if files != 1 {
				t.Fatalf("Scan parsed %d files, want 1 — the case inspected nothing", files)
			}
			var got []string
			for _, f := range findings {
				if f.Path != c.path {
					t.Errorf("finding %v reports path %q, want %q", f, f.Path, c.path)
				}
				got = append(got, f.Ref)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("findings = %q, want %q\n%s", got, c.want, c.src)
			}
		})
	}
}
