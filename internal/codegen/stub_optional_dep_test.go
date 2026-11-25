// File: internal/codegen/stub_optional_dep_test.go
//
// A `// forge:optional-dep` interface field got no Stub<Field> at all: the
// harness skipped optional fields outright, so the one test that exercises
// the field's non-nil path had to hand-roll a double for every such field —
// reliant's model_endpoint (Router, Credentials) and trigger (Syncer, Fires,
// ControlPlane, Sealer, Catalog, Intake, Access, Senders) handlers among
// them. The stub type is now emitted for those fields too, and NewTest<Svc>
// still leaves them nil: a test that says nothing about an optional dep runs
// the code's nil path.

package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// optionalDepHandler declares one required and one optional interface dep,
// both local, and methods that report which path the optional one takes.
const optionalDepHandler = `package relay

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
)

// Store is required.
type Store interface {
	Get(ctx context.Context, id string) (string, error)
}

// Notifier is optional: without it nothing is sent.
type Notifier interface {
	Notify(ctx context.Context, msg string) error
}

type Deps struct {
	Logger *slog.Logger
	Store  Store
	// Notifier tells the owner. Without it nothing is sent.
	// forge:optional-dep
	Notifier Notifier
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("relay: Store is required")
	}
	return &Service{deps: deps}, nil
}

func (s *Service) Register(*http.ServeMux, ...connect.HandlerOption) {}

// Tell reports whether a notification went out.
func (s *Service) Tell(ctx context.Context, msg string) (bool, error) {
	if s.deps.Notifier == nil {
		return false, nil
	}
	return true, s.deps.Notifier.Notify(ctx, msg)
}
`

// TestComputeAutoStubs_OptionalDepGetsAStubType: the optional field gets a
// stub, marked Optional so the harness does not install it; the required
// one is unchanged.
func TestComputeAutoStubs_OptionalDepGetsAStubType(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/relay",
		map[string]string{"internal/handlers/relay/service.go": optionalDepHandler})

	stubs, unresolved := computeAutoStubs(handlerDir, "relay")
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %+v, want none", unresolved)
	}
	got := map[string]DepsAutoStub{}
	for _, s := range stubs {
		got[s.FieldName] = s
	}
	if s, ok := got["Store"]; !ok || s.Optional {
		t.Errorf("Store stub = %+v (present %v), want a required stub", s, ok)
	}
	s, ok := got["Notifier"]
	if !ok {
		t.Fatalf("no stub for the forge:optional-dep Notifier field; stubs = %+v", stubs)
	}
	if !s.Optional || s.StubType != "StubNotifier" || len(s.Methods) != 1 {
		t.Errorf("Notifier stub = %+v, want Optional StubNotifier with Notify", s)
	}
}

// TestComputeAutoStubs_UnstubbableOptionalDepIsNoTODO: an optional field
// forge cannot stub is not required, so it earns no TODO in the harness.
func TestComputeAutoStubs_UnstubbableOptionalDepIsNoTODO(t *testing.T) {
	if testing.Short() {
		t.Skip("resolves a selector through the go toolchain; runs in task test")
	}
	handlerDir := writeStubFixtureModule(t, "internal/handlers/relay", map[string]string{
		"internal/handlers/relay/service.go": `package relay

import missing "example.com/proj/internal/missing"

type Deps struct {
	// forge:optional-dep
	Notifier missing.Notifier
}
`,
	})
	stubs, unresolved := computeAutoStubs(handlerDir, "relay")
	if len(stubs) != 0 || len(unresolved) != 0 {
		t.Errorf("stubs = %+v, unresolved = %+v; want neither for an optional field forge cannot read", stubs, unresolved)
	}
}

// TestGenerateBootstrapTesting_OptionalDepStubCompiles generates the real
// helpers_gen_test.go and runs a test against it: NewTestRelay leaves the
// optional Notifier nil (the nil path runs), and a StubNotifier passed
// through WithRelayDeps is the one the service calls.
func TestGenerateBootstrapTesting_OptionalDepStubCompiles(t *testing.T) {
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
	files := map[string]string{
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
		"gen/services/relay/v1/relayv1connect/relay.connect.go": `package relayv1connect

import "connectrpc.com/connect"

type RelayServiceClient interface{}

func NewRelayServiceClient(connect.HTTPClient, string, ...connect.ClientOption) RelayServiceClient {
	return nil
}
`,
		"internal/handlers/relay/service.go": optionalDepHandler,
		"internal/handlers/relay/optional_use_test.go": `package relay_test

import (
	"context"
	"testing"

	"example.com/proj/internal/handlers/relay"
)

func TestOptionalNotifierStaysNil(t *testing.T) {
	svc := relay.NewTestRelay(t)
	if sent, err := svc.Tell(context.Background(), "hi"); sent || err != nil {
		t.Fatalf("Tell = %v, %v; NewTestRelay must leave the optional Notifier nil", sent, err)
	}
}

func TestOptionalNotifierThroughDeps(t *testing.T) {
	var got string
	n := &relay.StubNotifier{NotifyFunc: func(_ context.Context, msg string) error {
		got = msg
		return nil
	}}
	svc := relay.NewTestRelay(t, relay.WithRelayDeps(relay.Deps{Notifier: n}))
	if sent, err := svc.Tell(context.Background(), "hi"); !sent || err != nil || got != "hi" {
		t.Fatalf("Tell = %v, %v (got %q); want the programmed StubNotifier", sent, err, got)
	}
	if n.CallCount("Notify") != 1 {
		t.Fatalf("Notify calls = %d, want 1", n.CallCount("Notify"))
	}
}
`,
	}
	root := writeHarnessFixture(t, files)
	helpers := readHarness(t, root, "internal/handlers/relay",
		[]ServiceDef{{Name: "RelayService", ModulePath: "example.com/proj"}}, nil)

	if !strings.Contains(helpers, "type StubNotifier struct") {
		t.Fatalf("no StubNotifier in the harness:\n%s", helpers)
	}
	if strings.Contains(helpers, "deps.Notifier = &StubNotifier{}") {
		t.Errorf("the harness installs the optional Notifier by default:\n%s", helpers)
	}
	if !strings.Contains(helpers, "deps.Store = &StubStore{}") {
		t.Errorf("the required Store stub is no longer installed:\n%s", helpers)
	}
	cmd := exec.Command("go", "test", "-count=1", "./internal/handlers/relay/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, terr := cmd.CombinedOutput(); terr != nil {
		t.Fatalf("the generated harness does not compile or behave: %v\n%s\n---- %s ----\n%s",
			terr, out, componentTestHelperFile, helpers)
	}
}
