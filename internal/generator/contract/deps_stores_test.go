package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The scaffolded contract_test.go tells every user, verbatim, that a
// `Store` dep interface "has a `pipeline.MockStore` sitting in the file
// next to this one". Before these tests that symbol did not exist and
// could not: mocks came only from interfaces declared in the package's
// own contract.go, while the db.*Store interfaces the service-layer
// skill tells users to declare live in the generated internal/db/
// store_gen.go.
//
// The cost of the gap was measured in a real greenfield build: three
// sub-agents each spent turns disbelieving the comment, and one gave up
// and hand-wrote 90 lines of fakes with eleven panic("not used")
// methods — precisely the hand-rolled fake the same comment forbids.
// These tests pin the comment as TRUE.

// depsProject writes a throwaway project with an internal/db package
// holding generated-store-shaped interfaces, and a consuming package
// whose Deps names some of them. depsBody is spliced into the consuming
// package's `type Deps struct` so each test picks which stores it names.
//
// The go.mod matters: the foreign package is resolved by import path
// against the module, so a test with no go.mod would exercise a
// different (and wrong) code path.
func depsProject(t *testing.T, depsBody string) string {
	t.Helper()
	root := t.TempDir()

	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("go.mod", "module example.com/proj\n\ngo 1.22\n")

	// Shaped exactly like a real internal/db/store_gen.go: per-entity
	// stores, an aggregate that returns them, orm.Context on WithTx.
	write("internal/db/store_gen.go", `package db

import (
	"context"

	"example.com/proj/pkg/orm"
)

type Estimate struct{ ID string }

type Job struct{ ID string }

type EstimateStore interface {
	CreateEstimate(ctx context.Context, msg *Estimate) error
	GetEstimateByID(ctx context.Context, id string) (*Estimate, error)
	ListEstimate(ctx context.Context, opts ...orm.QueryOption) ([]*Estimate, error)
	CountEstimate(ctx context.Context, opts ...orm.QueryOption) (int64, error)
	WithTx(tx orm.Context) EstimateStore
}

type JobStore interface {
	CreateJob(ctx context.Context, msg *Job) error
	GetJobByID(ctx context.Context, id string) (*Job, error)
	WithTx(tx orm.Context) JobStore
}

type Store interface {
	Estimates() EstimateStore
	Jobs() JobStore
	WithTx(tx orm.Context) Store
}
`)

	write("pkg/orm/orm.go", `package orm

import "context"

type Context interface {
	ExecContext(ctx context.Context, query string, args ...any) error
}

type QueryOption func(*Query)

type Query struct{ Limit int }
`)

	write("internal/pipeline/contract.go", `package pipeline

import "context"

// Service is the pipeline domain boundary.
type Service interface {
	Approve(ctx context.Context, id string) error
}
`)

	write("internal/pipeline/service.go", `package pipeline

import (
	"example.com/proj/internal/db"
)

// Deps holds dependencies for the pipeline package.
type Deps struct {
`+depsBody+`
}
`)

	return root
}

// skipTypeCheckUnderShort gates a test whose Generate type-checks the
// fixture's foreign packages: the Resolver runs `go list -export`, which
// compiles them (1-6s on a loaded machine, against the -short tier's 2s
// budget). Foreign mocks are rendered from go/types in production, so a
// syntax-only stand-in under -short would assert something forge no longer
// does — the test is gated, not gutted.
func skipTypeCheckUnderShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: Generate type-checks the fixture via `go list -export`; runs in task test")
	}
}

// buildDepsProject compiles the whole throwaway project, which is the
// assertion that actually matters: a mock for a foreign interface has to
// re-qualify every type in the method signatures (*Estimate is *db.Estimate
// once it is rendered into package pipeline), and only the compiler
// notices when that is wrong.
func buildDepsProject(t *testing.T, root string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping sandbox go build in -short mode")
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod"))
	if out, err := cmd.CombinedOutput(); err != nil {
		mock, _ := os.ReadFile(filepath.Join(root, "internal", "pipeline", "mock_gen.go"))
		t.Fatalf("go build of generated mock failed: %v\n%s\n---- mock_gen.go ----\n%s", err, out, mock)
	}
}

// TestGenerate_DepsStoreMock is the core claim: a Deps field typed as an
// interface from another package in this module produces a working mock
// in this package's mock_gen.go.
func TestGenerate_DepsStoreMock(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tEstimates db.EstimateStore\n")
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)

	// The mock the scaffolded comment promises, under the name it promises.
	assertContains(t, mockPath, "type MockEstimateStore struct {")
	assertContains(t, mockPath, "var _ db.EstimateStore = (*MockEstimateStore)(nil)")

	// Same ergonomics as every other generated mock: configure-by-field
	// plus the Recorder embed, so m.CallCount("GetEstimateByID") works
	// without any bookkeeping in the test.
	assertContains(t, mockPath, "contractkit.Recorder")
	assertContains(t, mockPath, "GetEstimateByIDFunc func(context.Context, string) (*db.Estimate, error)")
	assertContains(t, mockPath, `contractkit.MockNotSet("MockEstimateStore", "GetEstimateByID")`)

	// Types from the foreign package must be qualified. An unqualified
	// *Estimate is the failure mode that only the compiler catches.
	assertNotContains(t, mockPath, "*Estimate,")
	assertNotContains(t, mockPath, "(*Estimate, error)")

	// WithTx returns the interface itself — it must collapse to nil, not
	// the invalid composite literal db.EstimateStore{}.
	assertNotContains(t, mockPath, "db.EstimateStore{}")

	// The package's OWN contract interface is still mocked.
	assertContains(t, mockPath, "type MockService struct {")

	buildDepsProject(t, root)
}

// TestGenerate_DepsStoreMockIsProportionate pins the other half of the
// scope: output tracks what the package's Deps actually names. A package
// depending on one store must not acquire mocks for every other store in
// internal/db — that is how a generated file becomes noise nobody reads.
func TestGenerate_DepsStoreMockIsProportionate(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tEstimates db.EstimateStore\n")
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertNotContains(t, mockPath, "MockJobStore")
	assertNotContains(t, mockPath, "type MockStore struct {")
}

// TestGenerate_DepsAggregateStoreMock covers the aggregate: a package
// holding db.Store needs a USABLE mock, and MockStore is only usable if
// the stores its accessors return are mockable too. Reachability from the
// named interface is what bounds this — not "every store in the package".
func TestGenerate_DepsAggregateStoreMock(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tStore db.Store\n")
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)

	assertContains(t, mockPath, "type MockStore struct {")
	assertContains(t, mockPath, "var _ db.Store = (*MockStore)(nil)")
	assertContains(t, mockPath, "EstimatesFunc func() db.EstimateStore")

	// Transitively reachable, so a test can actually script the accessor.
	assertContains(t, mockPath, "type MockEstimateStore struct {")
	assertContains(t, mockPath, "type MockJobStore struct {")

	buildDepsProject(t, root)
}

// TestGenerate_DepsForeignNameCollision pins the case forge's OWN
// internal/docs hits: it declares Service in contract.go and names
// contract.Service in Deps. Both want to be "MockService", and two
// identically named types in one file do not compile — so the foreign
// one is prefixed with its package. Caught by probing this change
// against forge itself before shipping it, not in review.
func TestGenerate_DepsForeignNameCollision(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tEstimates db.EstimateStore\n\tPeer db.Service\n")

	// Give the foreign package a Service too, colliding with the local one.
	storePath := filepath.Join(root, "internal", "db", "store_gen.go")
	body, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read store_gen.go: %v", err)
	}
	body = append(body, []byte("\ntype Service interface {\n\tPing(ctx context.Context) error\n}\n")...)
	if err := os.WriteFile(storePath, body, 0644); err != nil {
		t.Fatalf("write store_gen.go: %v", err)
	}

	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)

	// The local one keeps the plain name; the foreign one is qualified.
	assertContains(t, mockPath, "type MockService struct {")
	assertContains(t, mockPath, "var _ Service = (*MockService)(nil)")
	assertContains(t, mockPath, "type MockDbService struct {")
	assertContains(t, mockPath, "var _ db.Service = (*MockDbService)(nil)")
	assertContains(t, mockPath, `contractkit.MockNotSet("MockDbService", "Ping")`)

	// A non-colliding foreign interface must NOT be renamed — the
	// scaffolded comment promises MockEstimateStore by that name.
	assertContains(t, mockPath, "type MockEstimateStore struct {")
	assertNotContains(t, mockPath, "MockDbEstimateStore")

	buildDepsProject(t, root)
}

// TestGenerate_DepsNonInterfaceFieldsIgnored keeps the discovery honest:
// only interfaces from other packages in this module are mocked. Logger
// and Config are concrete carve-outs, func fields are seams, and a
// stdlib type is not ours to mock.
func TestGenerate_DepsNonInterfaceFieldsIgnored(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tEstimates db.EstimateStore\n\tRow *db.Estimate\n")
	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "type MockEstimateStore struct {")

	// db.Estimate is a struct. A mock for it would not compile.
	assertNotContains(t, mockPath, "MockEstimate struct")
}

// TestGenerate_DepsForeignUnnamedParamsAndAliasCollision reproduces the
// protoc-gen-connect-go shape: foreign client methods omit parameter names and
// use an explicit `v1` alias inside a generic request type. The consuming
// contract already imports that same messages package as `messagesv1`, so the
// generated mock must use one alias consistently and invent concrete names it
// can record and forward.
func TestGenerate_DepsForeignUnnamedParamsAndAliasCollision(t *testing.T) {
	skipTypeCheckUnderShort(t)
	root := depsProject(t, "\tClient clientconnect.Client\n")

	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("gen/messages/v1/messages.go", `package messagesv1

type Request struct{}
type Response struct{}
`)
	write("pkg/transport/transport.go", `package transport

type Request[T any] struct{ Msg T }
type Response[T any] struct{ Msg T }
`)
	write("gen/client/v1/clientconnect/client.go", `package clientconnect

import (
	context "context"
	v1 "example.com/proj/gen/messages/v1"
	transport "example.com/proj/pkg/transport"
)

type Client interface {
	Fetch(context.Context, *transport.Request[v1.Request]) (*transport.Response[v1.Response], error)
}
`)
	write("internal/pipeline/contract.go", `package pipeline

import (
	"context"

	messagesv1 "example.com/proj/gen/messages/v1"
	"example.com/proj/pkg/transport"
)

type Service interface {
	Validate(ctx context.Context, req *transport.Request[messagesv1.Request]) error
}
`)
	write("internal/pipeline/service.go", `package pipeline

import clientconnect "example.com/proj/gen/client/v1/clientconnect"

type Deps struct {
	Client clientconnect.Client
}
`)

	contractPath := filepath.Join(root, "internal", "pipeline", "contract.go")
	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(root, "internal", "pipeline", "mock_gen.go")
	assertValidGo(t, mockPath)
	body, err := os.ReadFile(mockPath)
	if err != nil {
		t.Fatalf("read mock: %v", err)
	}
	src := string(body)
	assertContains(t, mockPath, "FetchFunc func(context.Context, *transport.Request[messagesv1.Request]) (*transport.Response[messagesv1.Response], error)")
	assertContains(t, mockPath, "func (m *MockClient) Fetch(p0 context.Context, p1 *transport.Request[messagesv1.Request])")
	assertContains(t, mockPath, `m.Recorder.Record("Fetch", p0, p1)`)
	assertContains(t, mockPath, `return m.FetchFunc(p0, p1)`)
	assertNotContains(t, mockPath, "*transport.Request[v1.Request]")
	assertNotContains(t, mockPath, "*transport.Response[v1.Response]")
	if got := strings.Count(src, `"example.com/proj/gen/messages/v1"`); got != 1 {
		t.Fatalf("messages import count = %d, want 1\n%s", got, src)
	}

	buildDepsProject(t, root)
}
