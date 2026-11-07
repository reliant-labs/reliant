package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Imports a mock needs are a fact about the packages its signatures
// reference, not about the spelling of their import paths.
//
// Observed in control-plane: internal/svcdaemon's Deps names
// natsio.Requester, whose RequestWithContext returns *nats.Msg from
// github.com/nats-io/nats.go. svcdaemon's contract.go never imports that
// package, and the generator guessed every unaliased import's name from the
// last path element — "nats.go" — so it never matched the `nats.` the
// signature uses. `forge generate --steps mocks` then wrote a mock_gen.go
// that mentions nats.Msg with no import for it, and the package stopped
// building. These tests pin the import set to the TYPE-CHECKED identity of
// each referenced package.

// writeTree writes body under root/rel, creating parents.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// natsProject is depsProject plus a third-party module shaped like
// github.com/nats-io/nats.go: an import path whose last element ("nats.go")
// is not the package's name ("nats"). It is wired through a directory
// replace so the fixture needs no network and no go.sum.
func natsProject(t *testing.T) string {
	t.Helper()
	root := depsProject(t, "")
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/proj\n\ngo 1.22\n\n" +
			"require github.com/nats-io/nats.go v1.37.0\n\n" +
			"replace github.com/nats-io/nats.go => ./third_party/nats.go\n",
		"third_party/nats.go/go.mod": "module github.com/nats-io/nats.go\n\ngo 1.22\n",
		"third_party/nats.go/nats.go": `package nats

type Msg struct {
	Subject string
	Data    []byte
}
`,
		"internal/natsio/natsio.go": `package natsio

import (
	"context"
	"io"

	"github.com/nats-io/nats.go"
)

// Closer is embedded below: a method set the generator can only see
// completely by type-checking, not by walking one interface's syntax.
type Closer interface {
	io.Closer
}

// Requester is the one method svcdaemon needs from a NATS connection.
type Requester interface {
	Closer
	RequestWithContext(ctx context.Context, subj string, data []byte) (*nats.Msg, error)
}
`,
		"internal/pipeline/service.go": `package pipeline

import "example.com/proj/internal/natsio"

// Deps holds dependencies for the pipeline package.
type Deps struct {
	NATSConn natsio.Requester
}
`,
	})
	return root
}

// mockImports parses mock_gen.go and returns local name → import path for
// every import, naming unaliased imports by the package name the fixture
// declares (the test knows them; the generator must discover them).
func mockImports(t *testing.T, mockPath string, declared map[string]string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), mockPath, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", mockPath, err)
	}
	out := map[string]string{}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := declared[path]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "" {
			name = path[strings.LastIndexByte(path, '/')+1:]
		}
		out[name] = path
	}
	return out
}

// assertEveryQualifierImported fails when mock_gen.go uses a package
// qualifier no import binds — the exact "undefined: nats" build break —
// or imports a package it never uses.
func assertEveryQualifierImported(t *testing.T, mockPath string, declared map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mockPath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", mockPath, err)
	}
	imports := mockImports(t, mockPath, declared)
	used := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
			used[id.Name] = true
		}
		return true
	})
	body, _ := os.ReadFile(mockPath)
	for name := range used {
		if _, ok := imports[name]; !ok && isLikelyPackageQualifier(file, name) {
			t.Errorf("mock_gen.go uses %s.<T> but imports nothing named %q\n%s", name, name, body)
		}
	}
	for name, path := range imports {
		if !used[name] {
			t.Errorf("mock_gen.go imports %q as %s but never uses it\n%s", path, name, body)
		}
	}
}

// isLikelyPackageQualifier separates a package qualifier from a selector on
// a local value (m.Recorder, p0.Foo): a name declared anywhere in the file as
// a receiver, parameter or variable is not a package.
func isLikelyPackageQualifier(file *ast.File, name string) bool {
	local := false
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			for _, id := range x.Names {
				if id.Name == name {
					local = true
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
					local = true
				}
			}
		}
		return !local
	})
	return !local
}

var natsDeclared = map[string]string{"github.com/nats-io/nats.go": "nats"}

// TestGenerate_ForeignSignatureImportsPackageTheContractNeverImports is the
// control-plane svcdaemon repro: the foreign interface's signature names a
// package that (a) contract.go does not import and (b) is not named after
// its import path's last element.
func TestGenerate_ForeignSignatureImportsPackageTheContractNeverImports(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := natsProject(t)
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "type MockRequester struct {")
	assertContains(t, mockPath, "var _ natsio.Requester = (*MockRequester)(nil)")
	assertContains(t, mockPath, "RequestWithContextFunc func(context.Context, string, []byte) (*nats.Msg, error)")
	assertContains(t, mockPath, `"github.com/nats-io/nats.go"`)
	// The real package name binds the import; no invented alias needed.
	assertNotContains(t, mockPath, `nats "github.com/nats-io/nats.go"`)

	// The embedded io.Closer is part of Requester's method set. A mock
	// without Close does not satisfy natsio.Requester.
	assertContains(t, mockPath, "func (m *MockRequester) Close() error {")

	assertEveryQualifierImported(t, mockPath, natsDeclared)
	buildDepsProject(t, root)
}

// TestGenerate_ContractImportNamedUnlikeItsPath covers the same defect on
// the contract's OWN interfaces: contract.go imports nats.go unaliased and
// uses nats.Msg, and the mock must keep that import.
func TestGenerate_ContractImportNamedUnlikeItsPath(t *testing.T) {
	root := natsProject(t)
	writeTree(t, root, map[string]string{
		"internal/pipeline/contract.go": `package pipeline

import (
	"context"

	"github.com/nats-io/nats.go"
)

// Service is the pipeline domain boundary.
type Service interface {
	Last(ctx context.Context, subject string) (*nats.Msg, error)
}
`,
		// No foreign deps: this test isolates contract.go's own imports.
		"internal/pipeline/service.go": "package pipeline\n\n// Deps holds dependencies for the pipeline package.\ntype Deps struct{}\n",
	})
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "LastFunc func(context.Context, string) (*nats.Msg, error)")
	assertContains(t, mockPath, `"github.com/nats-io/nats.go"`)
	assertEveryQualifierImported(t, mockPath, natsDeclared)
	buildDepsProject(t, root)
}

// TestGenerate_ImportAliasIsNotMatchedInsideALongerQualifier pins token-aware
// import detection: an explicit `v1` alias must not be pulled in just because
// a signature mentions `controlplanev1.` — an unused import does not compile.
func TestGenerate_ImportAliasIsNotMatchedInsideALongerQualifier(t *testing.T) {
	root := depsProject(t, "")
	writeTree(t, root, map[string]string{
		"gen/a/v1/a.go":                "package a\n\ntype Thing struct{}\n",
		"gen/controlplane/v1/types.go": "package controlplanev1\n\ntype Plan struct{}\n",
		"internal/pipeline/contract.go": `package pipeline

import (
	"context"

	v1 "example.com/proj/gen/a/v1"
	controlplanev1 "example.com/proj/gen/controlplane/v1"
)

var _ v1.Thing

// Service is the pipeline domain boundary.
type Service interface {
	Plan(ctx context.Context) (*controlplanev1.Plan, error)
}
`,
		"internal/pipeline/service.go": "package pipeline\n\n// Deps holds dependencies for the pipeline package.\ntype Deps struct{}\n",
	})
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)
	assertNotContains(t, mockPath, `v1 "example.com/proj/gen/a/v1"`)
	assertEveryQualifierImported(t, mockPath, nil)
	buildDepsProject(t, root)
}

// TestGenerate_ForeignMockKeepsDeclaredMethodOrder pins that rendering a
// foreign interface from go/types does not reshuffle an existing mock:
// go/types orders methods by name, the syntax path emitted them in source
// order, and every committed mock_gen.go in the wild is in source order.
func TestGenerate_ForeignMockKeepsDeclaredMethodOrder(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tEstimates db.EstimateStore\n")
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "internal", "pipeline", "mock_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	// Declared: Create, GetByID, List, Count, WithTx. Sorted would put
	// Count first.
	order := []string{
		"func (m *MockEstimateStore) CreateEstimate(",
		"func (m *MockEstimateStore) GetEstimateByID(",
		"func (m *MockEstimateStore) ListEstimate(",
		"func (m *MockEstimateStore) CountEstimate(",
		"func (m *MockEstimateStore) WithTx(",
	}
	last := -1
	for _, want := range order {
		at := strings.Index(src, want)
		if at < 0 {
			t.Fatalf("mock is missing %q\n%s", want, src)
		}
		if at < last {
			t.Fatalf("%q is out of declared order\n%s", want, src)
		}
		last = at
	}
}
