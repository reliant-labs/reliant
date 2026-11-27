package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// The reading of a project's formatter gate that `forge lint --fix`, narrow-deps
// and every codemod share. Config shapes are the ones real projects have:
// control-plane (v2, module prefix), reliant (v2, goimports and no prefixes),
// v1 string prefixes, gci, and no config at all.
func TestRewriteGoFormat(t *testing.T) {
	for _, tc := range []struct {
		name, file, config string
		want               GoFormatStandard
	}{
		{
			name: "v2 goimports with a prefix list (control-plane)", file: ".golangci.yml",
			config: "version: \"2\"\nformatters:\n  enable:\n    - goimports\n  settings:\n    goimports:\n      local-prefixes:\n        - example.com/thing\n        - example.org/other\n",
			want:   GoFormatStandard{GoImports: true, LocalPrefix: "example.com/thing,example.org/other", Declared: true},
		},
		{
			name: "v2 goimports without prefixes declares none (reliant): the module is NOT substituted", file: ".golangci.yml",
			config: "version: \"2\"\nformatters:\n  enable:\n    - goimports\n",
			want:   GoFormatStandard{GoImports: true, Declared: true},
		},
		{
			name: "v1 goimports with a comma string", file: ".golangci.yaml",
			config: "linters:\n  enable:\n    - goimports\nlinters-settings:\n  goimports:\n    local-prefixes: example.com/thing, example.org/other\n",
			want:   GoFormatStandard{GoImports: true, LocalPrefix: "example.com/thing,example.org/other", Declared: true},
		},
		{
			name: "gci owns grouping", file: ".golangci.yml",
			config: "version: \"2\"\nformatters:\n  enable:\n    - gci\n    - goimports\n",
			want:   GoFormatStandard{GroupingOwned: true, Declared: true},
		},
		{
			name: "a gate with only gofmt", file: ".golangci.yml",
			config: "version: \"2\"\nformatters:\n  enable:\n    - gofmt\n",
			want:   GoFormatStandard{Declared: true},
		},
		{
			name: "toml", file: ".golangci.toml",
			config: "[formatters]\nenable = [\"goimports\"]\n[formatters.settings.goimports]\nlocal-prefixes = [\"example.com/thing\"]\n",
			want:   GoFormatStandard{GoImports: true, LocalPrefix: "example.com/thing", Declared: true},
		},
		{
			name: "no config falls back to forge's convention: goimports with the module", file: "",
			want: GoFormatStandard{GoImports: true, LocalPrefix: "example.com/thing"},
		},
		{
			name: "a config that does not parse is no declaration", file: ".golangci.yml",
			config: "formatters: [unclosed\n",
			want:   GoFormatStandard{GoImports: true, LocalPrefix: "example.com/thing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/thing\n\ngo 1.22\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(root, tc.file), []byte(tc.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := RewriteGoFormat(root); got != tc.want {
				t.Fatalf("RewriteGoFormat = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// `forge lint --fix` formats to the project's declaration and, unlike a rewrite,
// never invents one: no config means gofmt alone.
func TestProjectGoFormat_NoConfigIsGofmtAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/thing\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProjectGoFormat(root); got != (GoFormatStandard{}) {
		t.Fatalf("ProjectGoFormat with no config = %+v, want the zero standard", got)
	}
}

// A config in an ancestor directory is the one golangci-lint loads.
func TestFindGolangciConfig_WalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".golangci.yml"), []byte("version: \"2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := FindGolangciConfig(nested), filepath.Join(root, ".golangci.yml"); got != want {
		t.Fatalf("FindGolangciConfig = %q, want %q", got, want)
	}
}
