// File: internal/codegen/stub_cross_pkg_embed_test.go
//
// A handler-local interface that EMBEDS another package's interface. reliant's
// inbox.Repository embeds worktreesweep.StorageStore, which embeds
// worktreesweep.Store; ParseLocalInterfaces skipped every selector embed, so
// the generated StubRepo had none of their methods and helpers_gen_test.go
// failed `forge generate`'s validate with "*StubRepo does not implement
// Repository (missing method AdoptWorktreeDaemon)".

package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// crossEmbedFiles is the inbox shape, one level deeper: the handler's
// Repository embeds sweep.StorageStore, which embeds sweep.Store (same
// package) AND audit.Log (a third package). Signatures reference a fourth
// package (core) the handler never imports, a type of the embedding package
// (sweep.CleanupResult), an interface result, and a method two embeds share.
func crossEmbedFiles() map[string]string {
	return map[string]string{
		"internal/core/core.go": `package core

type HeldWorktree struct{ ID string }
`,
		"internal/audit/audit.go": `package audit

import "context"

type Entry struct{ Msg string }

// Log is embedded two levels below the handler.
type Log interface {
	Record(ctx context.Context, e Entry) error
	// Shared is also declared by sweep.Store with an identical signature.
	Shared(ctx context.Context) error
}
`,
		"internal/sweep/sweep.go": `package sweep

import (
	"context"
	"io"
	"time"

	"example.com/proj/internal/audit"
	"example.com/proj/internal/core"
)

type CleanupResult struct{ ID string }

type Store interface {
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error
	ListAttached(ctx context.Context, userID string, stale time.Duration) ([]string, error)
	TryAdvisoryLock(ctx context.Context, key int64) (release func(), ok bool, err error)
	Shared(ctx context.Context) error
}

type StorageStore interface {
	Store
	audit.Log
	ListHeld(ctx context.Context, userID string) ([]*core.HeldWorktree, error)
	Results(ids ...string) []CleanupResult
	Open(name string) (io.Reader, error)
}
`,
	}
}

const crossEmbedHandler = `package inbox

import (
	"context"
	"log/slog"

	"example.com/proj/internal/sweep"
)

type Repository interface {
	sweep.StorageStore
	error

	ListInboxPending(ctx context.Context, userID string) ([]string, error)
}

type Deps struct {
	Logger *slog.Logger
	Repo   Repository
}
`

// crossEmbedWant is every method Repository has, through every embed.
var crossEmbedWant = []string{
	"AdoptWorktreeDaemon", "Error", "ListAttached", "ListHeld", "ListInboxPending",
	"Open", "Record", "Results", "Shared", "TryAdvisoryLock",
}

// TestParseLocalInterfaces_CrossPackageEmbedIsFlattened pins the parser: the
// cross-package embed contributes its whole method set (its own embeds at
// every depth included), a method reached twice appears once, and the
// signatures' packages arrive as imports under the names the stub spells.
func TestParseLocalInterfaces_CrossPackageEmbedIsFlattened(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the fixture module via the go toolchain; runs in task test")
	}
	files := crossEmbedFiles()
	files["internal/handlers/inbox/service.go"] = crossEmbedHandler
	handlerDir := writeStubFixtureModule(t, "internal/handlers/inbox", files)

	locals, err := ParseLocalInterfaces(handlerDir)
	if err != nil {
		t.Fatalf("ParseLocalInterfaces: %v", err)
	}
	repo, ok := locals["Repository"]
	if !ok {
		t.Fatalf("Repository not parsed; got %v", locals)
	}
	if len(repo.Unresolved) != 0 {
		t.Fatalf("every embed resolves in this fixture, got Unresolved=%v", repo.Unresolved)
	}
	var got []string
	byName := map[string]InterfaceMethod{}
	for _, m := range repo.Methods {
		got = append(got, m.Name)
		byName[m.Name] = m
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(crossEmbedWant, ",") {
		t.Fatalf("Repository methods = %v\nwant (each once)   %v", got, crossEmbedWant)
	}

	if r := byName["Results"]; r.Params != "...string" || r.Results != "[]sweep.CleanupResult" {
		t.Errorf("Results rendered (%s) %s; want the embedding file's sweep qualifier", r.Params, r.Results)
	}
	if r := byName["ListHeld"]; r.Results != "([]*core.HeldWorktree, error)" {
		t.Errorf("ListHeld results = %q", r.Results)
	}
	if r := byName["Open"]; r.ResultZeros[0] != "nil" {
		t.Errorf("an interface result must zero to nil, got %q", r.ResultZeros[0])
	}
	imports := map[string]string{}
	for _, ei := range repo.ExtraImports {
		imports[ei.Path] = ei.Alias
	}
	for path, alias := range map[string]string{
		"example.com/proj/internal/core":  "core",
		"example.com/proj/internal/sweep": "sweep",
		"example.com/proj/internal/audit": "audit",
		"io":                              "io",
		"time":                            "time",
		"context":                         "context",
	} {
		if imports[path] != alias {
			t.Errorf("ExtraImports[%s] = %q, want %q (all: %v)", path, imports[path], alias, imports)
		}
	}
}

// TestComputeAutoStubs_UnreadableEmbedLeavesFieldToTheTest: when the embedded
// package does not type-check, a stub would be missing its methods and not
// compile, so the field is reported unresolved (a TODO in the harness) rather
// than stubbed.
func TestComputeAutoStubs_UnreadableEmbedLeavesFieldToTheTest(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the fixture module via the go toolchain; runs in task test")
	}
	files := crossEmbedFiles()
	files["internal/sweep/broken.go"] = "package sweep\n\nvar _ = undefinedThing\n"
	files["internal/handlers/inbox/service.go"] = crossEmbedHandler
	handlerDir := writeStubFixtureModule(t, "internal/handlers/inbox", files)

	stubs, unresolved := computeAutoStubs(handlerDir, "inbox")
	if len(stubs) != 0 {
		t.Fatalf("a stub was emitted for a Repository whose embed could not be read: %+v", stubs)
	}
	if len(unresolved) != 1 || unresolved[0].FieldName != "Repo" ||
		!strings.Contains(unresolved[0].Reason, "sweep.StorageStore") {
		t.Fatalf("want Repo unresolved naming sweep.StorageStore, got %+v", unresolved)
	}
}

// TestGenerateBootstrapTesting_CrossPackageEmbedStubCompiles generates the
// REAL helpers_gen_test.go for the inbox-shaped fixture and runs a test
// against it: the harness compiles, StubRepo satisfies Repository, and every
// method the embeds contribute is programmable through its Func field.
func TestGenerateBootstrapTesting_CrossPackageEmbedStubCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs a fixture module with go test; runs in task test")
	}
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve forge root: %v", err)
	}
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	files := crossEmbedFiles()
	files["go.mod"] = "module example.com/proj\n\ngo " + pkgGoVersion(t, forgeRoot) + "\n\n" +
		"require github.com/reliant-labs/forge v0.0.0\n\n" +
		"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n"
	files["go.sum"] = string(sum)
	files["pkg/middleware/claims.go"] = `package middleware

import (
	"context"

	"github.com/reliant-labs/forge/pkg/auth"
)

type claimsKey struct{}

func ContextWithClaims(ctx context.Context, c *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}
`
	files["gen/services/inbox/v1/inboxv1connect/inbox.connect.go"] = `package inboxv1connect

import "connectrpc.com/connect"

type InboxServiceClient interface{}

func NewInboxServiceClient(connect.HTTPClient, string, ...connect.ClientOption) InboxServiceClient {
	return nil
}
`
	files["internal/handlers/inbox/service.go"] = crossEmbedHandler + `
type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) {
	if deps.Repo == nil {
		return nil, errors.New("inbox: Repo is required")
	}
	return &Service{deps: deps}, nil
}

func (s *Service) Register(*http.ServeMux, ...connect.HandlerOption) {}

func (s *Service) Adopt(ctx context.Context, id string) error {
	return s.deps.Repo.AdoptWorktreeDaemon(ctx, id, "d1")
}
`
	files["internal/handlers/inbox/service.go"] = strings.Replace(files["internal/handlers/inbox/service.go"],
		"import (\n\t\"context\"\n", "import (\n\t\"context\"\n\t\"errors\"\n\t\"net/http\"\n\n\t\"connectrpc.com/connect\"\n", 1)
	files["internal/handlers/inbox/embed_use_test.go"] = `package inbox_test

import (
	"context"
	"testing"

	"example.com/proj/internal/audit"
	"example.com/proj/internal/handlers/inbox"
)

var _ inbox.Repository = (*inbox.StubRepo)(nil)

func TestEmbeddedMethodsAreProgrammable(t *testing.T) {
	var adopted string
	repo := &inbox.StubRepo{
		AdoptWorktreeDaemonFunc: func(_ context.Context, id, _ string) error {
			adopted = id
			return nil
		},
	}
	svc := inbox.NewTestInbox(t, inbox.WithInboxDeps(inbox.Deps{Repo: repo}))
	if err := svc.Adopt(context.Background(), "w1"); err != nil || adopted != "w1" {
		t.Fatalf("Adopt = %v, adopted %q; want the programmed embed method", err, adopted)
	}
	// Two levels down, from a third package: unprogrammed, it refuses.
	if err := repo.Record(context.Background(), audit.Entry{}); err == nil {
		t.Fatal("an unprogrammed Record answered nil")
	}
}
`
	root := writeHarnessFixture(t, files)
	helpers := readHarness(t, root, "internal/handlers/inbox",
		[]ServiceDef{{Name: "InboxService", ModulePath: "example.com/proj"}}, nil)

	for _, m := range crossEmbedWant {
		if !strings.Contains(helpers, ") "+m+"(") {
			t.Errorf("StubRepo is missing %s", m)
		}
	}
	cmd := exec.Command("go", "test", "-count=1", "./internal/handlers/inbox/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, terr := cmd.CombinedOutput(); terr != nil {
		t.Fatalf("the generated harness does not compile or its stub is wrong: %v\n%s\n---- %s ----\n%s",
			terr, out, componentTestHelperFile, helpers)
	}
}
