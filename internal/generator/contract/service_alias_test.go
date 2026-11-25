package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/testhost"
)

// A contract that IS another interface, by alias — forge bug 75cd71d8:
// reliant's internal/changetools declares `type Service =
// tools.ChangeController`, its consumer's interface. The mock, the decorator
// and the test scaffold read the parse, so the alias must parse to the
// aliased interface's whole method set, and the mock must compile against
// the alias.

// aliasProject is depsProject with internal/tools holding the consumer-owned
// interface and an internal/adapter whose contract.go is body.
func aliasProject(t *testing.T, body string) (root, contractPath string) {
	t.Helper()
	root = depsProject(t, "")
	writeTree(t, root, map[string]string{
		"internal/tools/tools.go": `package tools

import (
	"context"

	"example.com/proj/internal/db"
)

// ChangeController is the consumer-owned contract.
type ChangeController interface {
	Apply(ctx context.Context, id string) (*db.Estimate, error)
	Close() error
}

// Controller is concrete.
type Controller struct{}
`,
		"internal/adapter/service.go":  "package adapter\n\n// Deps holds dependencies for the adapter package.\ntype Deps struct{}\n",
		"internal/adapter/contract.go": body,
	})
	return root, filepath.Join(root, "internal", "adapter", "contract.go")
}

// The mock and the observe decorator (middleware_gen.go) both render from
// the parse, and both must compile against the alias.
func TestGenerate_ServiceAliasOfAForeignInterface(t *testing.T) {
	skipTypeCheckUnderShort(t)
	forgeRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	root, contractPath := aliasProject(t, `package adapter

import "example.com/proj/internal/tools"

// Service is the consumer's contract.
type Service = tools.ChangeController
`)
	writeTree(t, root, map[string]string{
		"go.mod": "module example.com/proj\n\ngo 1.22\n\n" +
			"require github.com/reliant-labs/forge v0.0.0\n\n" +
			"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n",
		"go.sum": string(sum),
		"internal/adapter/observe_chain.go": `package adapter

import "github.com/reliant-labs/forge/pkg/observe"

func newObserveChain() *observe.ComponentChain { return nil }
`,
	})
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cf, err := ParseContract(contractPath)
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	adapter := filepath.Dir(contractPath)
	if _, err := WriteObservedDecorator(cf, adapter, "Service", "", "", "adapter", nil, Options{}); err != nil {
		t.Fatalf("WriteObservedDecorator: %v", err)
	}
	mockPath := filepath.Join(adapter, "mock_gen.go")
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "type MockService struct {")
	assertContains(t, mockPath, "func (m *MockService) Apply(")
	assertContains(t, mockPath, "func (m *MockService) Close(")
	assertContains(t, mockPath, "var _ Service = (*MockService)(nil)")
	assertContains(t, filepath.Join(adapter, "middleware_gen.go"), ") Apply(")
	buildAliasProject(t, root, mockPath)
}

func TestGenerate_ServiceAliasOfASiblingInterface(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root, contractPath := aliasProject(t, `package adapter

// Service is the gateway.
type Service = Gateway
`)
	writeTree(t, root, map[string]string{
		"internal/adapter/gateway.go": "package adapter\n\n// Gateway is declared in another file.\ntype Gateway interface{ Ping() error }\n",
	})
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mockPath := filepath.Join(root, "internal", "adapter", "mock_gen.go")
	assertContains(t, mockPath, "func (m *MockService) Ping(")
	buildAliasProject(t, root, mockPath)
}

// An alias of a type that is not an interface is not mocked: a mock of no
// methods would not satisfy it, and the package would not build.
func TestGenerate_ServiceAliasOfAStructIsNotMocked(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root, contractPath := aliasProject(t, `package adapter

import "example.com/proj/internal/tools"

// Service is concrete.
type Service = tools.Controller

// Helper keeps the mock file non-empty.
type Helper interface{ Do() }
`)
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mockPath := filepath.Join(root, "internal", "adapter", "mock_gen.go")
	assertNotContains(t, mockPath, "MockService")
	buildAliasProject(t, root, mockPath)
}

// buildAliasProject compiles the adapter package (with its mock) and what it
// imports; depsProject's pipeline, with an empty Deps, is not part of it.
func buildAliasProject(t *testing.T, root, mockPath string) {
	t.Helper()
	cmd := exec.Command("go", "build", "./internal/adapter/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		testhost.SkipIfModuleDownloadFailed(t, out)
		mock, _ := os.ReadFile(mockPath)
		t.Fatalf("generated mock does not compile: %v\n%s\n---- mock_gen.go ----\n%s", err, out, mock)
	}
}
