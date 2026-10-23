// File: internal/codegen/harness_unfilled_deps_test.go
//
// A required Deps field the harness cannot construct — reliant's account
// handler takes `SQL *sql.DB` — left NewTest<Svc>(t) dying in validateDeps,
// and that included the scaffolded test forge itself wrote beside the
// handler: a generated file that could never pass without an edit. These
// tests generate the real harness and scaffold test for such a service and
// run them.

package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// unfilledDepsService is reliant's account shape: a raw *sql.DB the harness
// cannot build, a func with no framework default, and an optional
// dependency that must stay out of it.
const unfilledDepsService = `package accounts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "example.com/proj/gen/services/accounts/v1"
)

type Deps struct {
	Logger *slog.Logger
	SQL    *sql.DB
	Notify func(userID string) error
	// forge:optional-dep
	Audit *sql.DB
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return errors.New("accounts: Deps.Logger is required")
	}
	if d.SQL == nil {
		return errors.New("accounts: Deps.SQL is required")
	}
	if d.Notify == nil {
		return errors.New("accounts: Deps.Notify is required")
	}
	return nil
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

func (s *Service) Register(*http.ServeMux, ...connect.HandlerOption) {}

func (s *Service) DeleteAccount(context.Context, *connect.Request[pb.DeleteAccountRequest]) (*connect.Response[pb.DeleteAccountResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("DeleteAccount"))
}
`

func unfilledDepsFixture(t *testing.T, forgeRoot string) string {
	t.Helper()
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	return writeHarnessFixture(t, map[string]string{
		"go.mod": "module example.com/proj\n\ngo " + pkgGoVersion(t, forgeRoot) + "\n\n" +
			"require github.com/reliant-labs/forge v0.0.0\n\n" +
			"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n",
		"go.sum": string(sum),
		"pkg/middleware/claims.go": `package middleware

import (
	"context"

	"github.com/reliant-labs/forge/pkg/auth"
)

type claimsKey struct{}

func ContextWithClaims(ctx context.Context, c *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}
`,
		"gen/services/accounts/v1/pb.go": `package accountsv1

type DeleteAccountRequest struct{}
type DeleteAccountResponse struct{}
`,
		"gen/services/accounts/v1/accountsv1connect/accounts.connect.go": `package accountsv1connect

import "connectrpc.com/connect"

type AccountsServiceClient interface{}

func NewAccountsServiceClient(connect.HTTPClient, string, ...connect.ClientOption) AccountsServiceClient {
	return nil
}
`,
		"internal/handlers/accounts/service.go": unfilledDepsService,
		"internal/handlers/accounts/hand_test.go": `package accounts_test

import (
	"testing"

	"example.com/proj/internal/handlers/accounts"
)

// Run on its own and expected to FAIL: the failure must say what to do.
func TestBareConstruction(t *testing.T) {
	accounts.NewTestAccounts(t)
}

func TestPlaceholdersConstruct(t *testing.T) {
	if accounts.NewTestAccounts(t, accounts.WithPlaceholderDeps()) == nil {
		t.Fatal("no service")
	}
}
`,
	})
}

func accountsServiceDef() ServiceDef {
	return ServiceDef{
		Name:       "AccountsService",
		ModulePath: "example.com/proj",
		GoPackage:  "example.com/proj/gen/services/accounts/v1",
		PkgName:    "accountsv1",
		Methods:    []Method{{Name: "DeleteAccount", InputType: "DeleteAccountRequest", OutputType: "DeleteAccountResponse"}},
	}
}

func runFixtureTests(t *testing.T, root, run string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", "test", "-count=1", "-run", run, "./internal/handlers/accounts/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestHarness_UnfilledConcreteDepsHaveAWayThrough is the finding. The
// scaffolded test forge writes for such a service passes as written, the
// harness offers WithPlaceholderDeps for it, and a bare NewTest<Svc>(t)
// fails naming the dependency and both ways to supply it.
func TestHarness_UnfilledConcreteDepsHaveAWayThrough(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs a fixture module with go test; runs in task test")
	}
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve forge root: %v", err)
	}
	root := unfilledDepsFixture(t, forgeRoot)
	svc := accountsServiceDef()
	helpers := readHarness(t, root, "internal/handlers/accounts", []ServiceDef{svc}, nil)

	handlerDir := filepath.Join(root, "internal", "handlers", "accounts")
	if err := writeScaffoldTests(handlerDir, mapServiceDefToTemplateData(svc, root)); err != nil {
		t.Fatalf("writeScaffoldTests: %v", err)
	}
	scaffold, err := os.ReadFile(filepath.Join(handlerDir, ScaffoldTestFileName("DeleteAccount")))
	if err != nil {
		t.Fatalf("read scaffold test: %v", err)
	}

	if out, err := runFixtureTests(t, root, "TestDeleteAccount_Generated|TestPlaceholdersConstruct"); err != nil {
		t.Fatalf("the scaffolded test must pass as written: %v\n%s\n---- scaffold ----\n%s\n---- %s ----\n%s",
			err, out, scaffold, componentTestHelperFile, helpers)
	}

	out, err := runFixtureTests(t, root, "TestBareConstruction")
	if err == nil {
		t.Fatalf("a bare NewTestAccounts constructed with Deps.SQL nil:\n%s", out)
	}
	for _, want := range []string{
		"accounts: Deps.SQL is required",         // validateDeps' own words
		"Deps.SQL (*sql.DB), Deps.Notify (func(", // what the harness cannot build
		"WithAccountsDeps(Deps{...})",            // the real fix
		"WithPlaceholderDeps()",                  // the scaffold-row fix
	} {
		if !strings.Contains(out, want) {
			t.Errorf("construction failure is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Deps.Audit") {
		t.Errorf("an optional dependency is not the harness's to fill:\n%s", out)
	}
}

// TestComputeUnfilledDeps pins the classification: only nil-able types the
// harness has no default for, never the ones it fills, never optional ones.
func TestComputeUnfilledDeps(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/accounts", map[string]string{
		"internal/handlers/accounts/service.go": `package accounts

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

type Settings struct{}

type Repository interface{ Get() error }

type Deps struct {
	Logger   *slog.Logger
	DB       orm.Context
	Now      func() time.Time
	NewID    func() string
	Repo     Repository
	Settings Settings
	SQL      *sql.DB
	Raw      *sql.DB
	Tags     map[string]string
	Notify   func(string) error
	// forge:optional-dep
	Audit *sql.DB
}
`,
	})
	got := UnfilledDepsFor(handlerDir)
	want := []string{
		"Deps.SQL (*sql.DB)",
		"Deps.Raw (*sql.DB)",
		"Deps.Tags (map[string]string)",
		"Deps.Notify (func(string) error)",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("UnfilledDepsFor =\n  %q\nwant\n  %q", got, want)
	}
}
