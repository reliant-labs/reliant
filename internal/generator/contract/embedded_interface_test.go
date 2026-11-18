package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// A contract interface that EMBEDS another package's interface. The parser
// skipped every `pkg.Iface` embed, so mock_gen.go and the observe decorator
// (middleware_gen.go) were missing the embedded methods and neither compiled:
// the mock fails its `var _ Service = (*MockService)(nil)` check, the
// decorator its `return &forgeMiddlewareService{...}` as a Service.

// embedProject is depsProject plus an embed two packages deep: Service embeds
// sweep.StorageStore, which embeds sweep.Store (same package) and audit.Log (a
// third package), plus the standard library's io.Closer and the predeclared
// error. Signatures reference packages contract.go never imports (core, time,
// audit), and Shared is reachable through two embeds.
func embedProject(t *testing.T) string {
	t.Helper()
	root := depsProject(t, "")
	writeTree(t, root, map[string]string{
		"internal/core/core.go": "package core\n\ntype HeldWorktree struct{ ID string }\n",
		"internal/audit/audit.go": `package audit

import "context"

type Entry struct{ Msg string }

type Log interface {
	Record(ctx context.Context, e Entry) error
	Shared(ctx context.Context) error
}
`,
		"internal/sweep/sweep.go": `package sweep

import (
	"context"
	"time"

	"example.com/proj/internal/audit"
	"example.com/proj/internal/core"
)

type Store interface {
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error
	ListAttached(ctx context.Context, userID string, stale time.Duration) ([]string, error)
	Shared(ctx context.Context) error
}

type StorageStore interface {
	Store
	audit.Log
	ListHeld(ctx context.Context, userID string) ([]*core.HeldWorktree, error)
}
`,
		"internal/pipeline/service.go": "package pipeline\n\n// Deps holds dependencies for the pipeline package.\ntype Deps struct{}\n",
		"internal/pipeline/contract.go": `package pipeline

import (
	"context"
	"io"

	"example.com/proj/internal/sweep"
)

// Service is the pipeline domain boundary.
type Service interface {
	sweep.StorageStore
	io.Closer
	error

	Run(ctx context.Context) error
}
`,
	})
	return root
}

var embedWant = []string{
	"AdoptWorktreeDaemon", "Close", "Error", "ListAttached", "ListHeld", "Record", "Run", "Shared",
}

// TestParseContract_CrossPackageEmbedIsFlattened pins the parse both
// generators render from.
func TestParseContract_CrossPackageEmbedIsFlattened(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := embedProject(t)
	cf, err := ParseContract(filepath.Join(root, "internal", "pipeline", "contract.go"))
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	var svc *InterfaceDef
	for i := range cf.Interfaces {
		if cf.Interfaces[i].Name == "Service" {
			svc = &cf.Interfaces[i]
		}
	}
	if svc == nil {
		t.Fatalf("Service not parsed: %+v", cf.Interfaces)
	}
	count := map[string]int{}
	for _, m := range svc.Methods {
		count[m.Name]++
	}
	for _, name := range embedWant {
		if count[name] != 1 {
			t.Errorf("Service has %s %d times, want once (methods: %v)", name, count[name], count)
		}
	}
	if len(count) != len(embedWant) {
		t.Errorf("Service methods = %v, want exactly %v", count, embedWant)
	}
}

// TestGenerate_CrossPackageEmbedMockAndDecoratorCompile generates mock_gen.go
// and middleware_gen.go for the embedding contract and compiles both.
func TestGenerate_CrossPackageEmbedMockAndDecoratorCompile(t *testing.T) {
	skipTypeCheckUnderShort(t)
	forgeRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	root := embedProject(t)
	writeTree(t, root, map[string]string{
		// The decorator imports forge/pkg/observe.
		"go.mod": "module example.com/proj\n\ngo 1.22\n\n" +
			"require github.com/reliant-labs/forge v0.0.0\n\n" +
			"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n",
		"go.sum": string(sum),
		// The owned seam the decorator calls.
		"internal/pipeline/observe_chain.go": `package pipeline

import "github.com/reliant-labs/forge/pkg/observe"

func newObserveChain() *observe.ComponentChain { return nil }
`,
	})
	pipeline := filepath.Join(root, "internal", "pipeline")
	contractPath := filepath.Join(pipeline, "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cf, err := ParseContract(contractPath)
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	if _, err := WriteObservedDecorator(cf, pipeline, "Service", "", "", "pipeline", nil, Options{}); err != nil {
		t.Fatalf("WriteObservedDecorator: %v", err)
	}

	mockPath := filepath.Join(pipeline, "mock_gen.go")
	for _, name := range embedWant {
		assertContains(t, mockPath, "func (m *MockService) "+name+"(")
	}
	assertEveryQualifierImported(t, mockPath, nil)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		mock, _ := os.ReadFile(mockPath)
		deco, _ := os.ReadFile(filepath.Join(pipeline, "middleware_gen.go"))
		t.Fatalf("generated mock/decorator do not compile: %v\n%s\n---- mock_gen.go ----\n%s\n---- middleware_gen.go ----\n%s",
			err, out, mock, deco)
	}
	deco, _ := os.ReadFile(filepath.Join(pipeline, "middleware_gen.go"))
	for _, name := range embedWant {
		if !strings.Contains(string(deco), ") "+name+"(") {
			t.Errorf("decorator is missing %s", name)
		}
	}
}
