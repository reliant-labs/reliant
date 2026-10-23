package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The format pre-pass applies the formatting the project's own gate checks,
// never forge's scaffold default in its place.
//
// Incident (reliant, re-pinning forge): `forge lint --quiet --scope
// internal/handlers/trigger --scope internal/grpc` regrouped the imports of
// six files under internal/grpc the author never touched. reliant's
// .golangci.yml enables goimports with no local-prefixes, and its pre-commit
// hook runs `go fmt`; both accepted those files as they were. The pre-pass
// formatted to goimports with the go.mod module as the local prefix — the
// scaffolded .golangci.yml's setting, which reliant does not have — so it
// rewrote files the project's own gate passes.

// mixedLocalImports is a file both `go fmt` and goimports-without-a-local-
// prefix accept: the module's own import sorted in with the third-party ones
// (reliant's internal/grpc/server_test.go).
const mixedLocalImports = `package foo

import (
	"testing"

	"connectrpc.com/connect"
	"example.com/thing/other"
	"github.com/stretchr/testify/require"
)

var _ = testing.T{}
var _ = connect.CodeOK
var _ = other.X
var _ = require.True
`

// gofmtDirty has gofmt-wrong indentation and nothing goimports would add.
const gofmtDirty = `package foo

func Use() {
        println("x")
}
`

func TestFormatGoTree_FormatsToTheProjectsGate(t *testing.T) {
	cases := []struct {
		name     string
		config   string // .golangci.yml; "" writes none
		file     string
		wantSame bool // the file must be left byte-for-byte as written
	}{
		{
			name:     "v2 goimports without local-prefixes leaves a module import in the third-party group",
			config:   "version: \"2\"\nformatters:\n  enable:\n    - goimports\n",
			file:     mixedLocalImports,
			wantSame: true,
		},
		{
			name:   "v2 goimports with local-prefixes splits the module's imports into their own group",
			config: "version: \"2\"\nformatters:\n  enable:\n    - goimports\n  settings:\n    goimports:\n      local-prefixes:\n        - example.com/thing\n",
			file:   mixedLocalImports,
		},
		{
			name:   "v1 goimports with a local-prefixes string splits them too",
			config: "linters:\n  enable:\n    - goimports\nlinters-settings:\n  goimports:\n    local-prefixes: example.com/thing\n",
			file:   mixedLocalImports,
		},
		{
			name:     "gci owns import grouping when it is enabled",
			config:   "version: \"2\"\nformatters:\n  enable:\n    - goimports\n    - gci\n  settings:\n    goimports:\n      local-prefixes:\n        - example.com/thing\n",
			file:     mixedLocalImports,
			wantSame: true,
		},
		{
			name:     "a gate with no import formatter gets no import regrouping",
			config:   "version: \"2\"\nformatters:\n  enable:\n    - gofmt\n",
			file:     mixedLocalImports,
			wantSame: true,
		},
		{
			name:     "no golangci config declares no import grouping",
			file:     mixedLocalImports,
			wantSame: true,
		},
		{
			name:   "gofmt applies whatever the gate declares",
			config: "version: \"2\"\nformatters:\n  enable:\n    - gofmt\n",
			file:   gofmtDirty,
		},
		{
			name: "gofmt applies with no config at all",
			file: gofmtDirty,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newBareFormatProject(t)
			if tc.config != "" {
				writeFile(t, root, ".golangci.yml", tc.config)
			}
			abs := writeFile(t, root, "internal/foo/foo.go", tc.file)

			changed, err := formatGoTree(root)
			if err != nil {
				t.Fatalf("formatGoTree: %v", err)
			}
			got, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantSame {
				if len(changed) != 0 || string(got) != tc.file {
					t.Fatalf("the pre-pass rewrote a file the project's gate accepts (changed=%v):\n%s", changed, got)
				}
				return
			}
			if len(changed) != 1 || string(got) == tc.file {
				t.Fatalf("the pre-pass did not apply the gate's formatting (changed=%v):\n%s", changed, got)
			}
		})
	}
}

// The local-prefix grouping the gate declares is the one applied: the
// module import lands in a trailing group of its own.
func TestFormatGoTree_AppliesTheDeclaredLocalPrefix(t *testing.T) {
	root := newFormatProject(t) // the scaffolded gate: goimports, module prefix
	abs := writeFile(t, root, "internal/foo/foo.go", mixedLocalImports)
	if _, err := formatGoTree(root); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(abs)
	if !strings.Contains(string(got), "\"github.com/stretchr/testify/require\"\n\n\t\"example.com/thing/other\"\n)") {
		t.Fatalf("module import not split into its own trailing group:\n%s", got)
	}
}

// A scoped pre-pass touches only files inside the scope (`forge lint
// --scope`): a file outside it is left as it was, even one the gate would
// reformat.
func TestFormatGoTreeFiltered_TouchesOnlyTheScope(t *testing.T) {
	root := newFormatProject(t)
	inside := writeFile(t, root, "internal/handlers/trigger/a.go", gofmtDirty)
	outside := writeFile(t, root, "internal/grpc/b.go", gofmtDirty)
	scope, err := parseLintScope(root, []string{"internal/handlers/trigger"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := formatGoTreeFiltered(root, scope.contains)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || filepath.ToSlash(changed[0]) != "internal/handlers/trigger/a.go" {
		t.Fatalf("changed = %v, want only the in-scope file", changed)
	}
	if got, _ := os.ReadFile(inside); string(got) == gofmtDirty {
		t.Error("the in-scope file was not formatted")
	}
	if got, _ := os.ReadFile(outside); string(got) != gofmtDirty {
		t.Errorf("a file outside --scope was rewritten:\n%s", got)
	}
}

// `forge lint --help` says that lint rewrites files by default and how to
// stop it, where an agent reading the synopsis will see it.
func TestLintHelpSaysItIsReportOnly(t *testing.T) {
	cmd := newCmd(testFactory())
	if !strings.Contains(cmd.Short, "report-only") || !strings.Contains(cmd.Short, "--fix") {
		t.Errorf("Short does not say lint is report-only unless --fix: %q", cmd.Short)
	}
	for _, flag := range []string{"scope", "quiet"} {
		if usage := cmd.Flags().Lookup(flag).Usage; !strings.Contains(usage, "--fix") {
			t.Errorf("--%s usage does not say what --fix does under it: %q", flag, usage)
		}
	}
	long := strings.Join(strings.Fields(cmd.Long), " ")
	for _, want := range []string{
		"REPORT-ONLY BY DEFAULT",
		"REWRITING FILES IN PLACE",
		"never rewrites a file the project's own formatter gate accepts",
		"never applies golangci-lint's fixes",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("Long lacks %q:\n%s", want, cmd.Long)
		}
	}
}
