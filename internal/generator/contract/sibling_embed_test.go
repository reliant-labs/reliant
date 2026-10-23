package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/depsurface"
	"github.com/reliant-labs/forge/internal/goexec"
)

// A deps.go role that embeds a role ANOTHER file of the same package
// declares. The generator read only the file it generates from, so the
// embedded methods resolved to nothing: the mock came out without them and
// generate's build validation failed with "does not implement". The embed is
// now resolved from the type-checked package, the way #1109 resolves an
// embed from another package — with the package's own types unqualified
// (a mock importing its own package is an import cycle) and the sibling
// file's imports carried over.
//
//	roles.go:  type Reader interface { Get(ctx, id ItemID) (*Item, error); Since(t time.Time) []Item }
//	deps.go:   type Repo interface { Reader; Put(ctx, it *Item) error }
//	           type Audited interface { Repo; Audit() string }   (two hops)
func siblingEmbedProject(t *testing.T) (root, depsPath string) {
	t.Helper()
	root = t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/proj\n\ngo 1.22\n",
		"internal/store/item.go": `package store

type ItemID string

type Item struct{ ID ItemID }
`,
		"internal/store/roles.go": `package store

import (
	"context"
	stdtime "time"
)

// Reader is declared in its own file, beside the types it returns.
type Reader interface {
	Get(ctx context.Context, id ItemID) (*Item, error)
	Since(t stdtime.Time) []Item
}
`,
		"internal/store/deps.go": depsurface.DepsFileMarker + `

package store

import "context"

// Repo embeds Reader, which roles.go declares.
type Repo interface {
	Reader
	Put(ctx context.Context, it *Item) error
}

// Audited embeds Repo, so it reaches Reader through two hops.
type Audited interface {
	Repo
	Audit() string
}
`,
	})
	return root, filepath.Join(root, "internal", "store", "deps.go")
}

func TestParseContract_SiblingFileEmbedIsResolved(t *testing.T) {
	skipTypeCheckUnderShort(t)
	_, depsPath := siblingEmbedProject(t)
	cf, err := ParseContract(depsPath)
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	want := map[string][]string{
		"Repo":    {"Put", "Get", "Since"},
		"Audited": {"Audit", "Put", "Get", "Since"},
	}
	for _, iface := range cf.Interfaces {
		names, ok := want[iface.Name]
		if !ok {
			continue
		}
		delete(want, iface.Name)
		var got []string
		for _, m := range iface.Methods {
			got = append(got, m.Name)
		}
		if strings.Join(got, ",") != strings.Join(names, ",") {
			t.Errorf("%s methods = %v, want %v", iface.Name, got, names)
		}
		for _, m := range iface.Methods {
			for _, p := range append(append([]ParamDef{}, m.Params...), m.Results...) {
				if strings.Contains(p.TypeExpr, "store.") {
					t.Errorf("%s.%s renders its own package qualified (%s): the mock would import itself", iface.Name, m.Name, p.TypeExpr)
				}
			}
		}
	}
	if len(want) != 0 {
		t.Errorf("interfaces not parsed: %v", want)
	}
}

// The real claim: the generated mock compiles, and implements both roles.
func TestGenerate_SiblingFileEmbedMockCompiles(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root, depsPath := siblingEmbedProject(t)
	if err := Generate(depsPath); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mockPath := filepath.Join(root, "internal", "store", "mock_gen.go")
	for _, m := range []string{"Get", "Since", "Put", "Audit"} {
		assertContains(t, mockPath, ") "+m+"(")
	}
	assertEveryQualifierImported(t, mockPath, nil)
	// The header names the file the roles were read from.
	assertContains(t, mockPath, "// Source: deps.go in this package.")
	assertContains(t, mockPath, "// To customize: edit deps.go (")
	// Prove the mocks satisfy the roles, beyond their own var _ checks.
	writeTree(t, root, map[string]string{
		"internal/store/assert_test_only.go": `package store

var (
	_ Repo    = (*MockRepo)(nil)
	_ Audited = (*MockAudited)(nil)
)
`,
	})
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		mock, _ := os.ReadFile(mockPath)
		t.Fatalf("generated mock does not compile: %v\n%s\n---- mock_gen.go ----\n%s", err, out, mock)
	}
}

// The mock header names the file it was generated from — deps.go for a
// package whose roles live there — and contract.go otherwise.
func TestRenderMock_HeaderNamesItsSource(t *testing.T) {
	for source, want := range map[string]string{"deps.go": "deps.go", "contract.go": "contract.go", "": "contract.go"} {
		cf := &File{Package: "store", Source: source, Interfaces: []InterfaceDef{{Name: "Repo"}}}
		out, err := renderMock(cf, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{"// Source: " + want + " in this package.", "// To customize: edit " + want + " ("} {
			if !strings.Contains(string(out), line) {
				t.Errorf("Source %q: header lacks %q:\n%s", source, line, out)
			}
		}
	}
}
