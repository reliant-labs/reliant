// File: internal/codegen/stub_programmable_test.go
//
// The auto-stub as a programmable double. The harness used to answer every
// (T, error) lookup with (nil, nil), which a handler reads as "found" and
// dereferences — so a not-found or error branch could not be reached through
// the stub, and every reliant P3b lane hand-wrote a fake instead. These
// tests generate the REAL helpers_gen_test.go for a fixture service and run
// a test against it, so they pin what a project's test actually sees.

package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// programmableStubFixture is a service whose Deps carry a local interface
// covering every method shape the stub must render — a lookup, an error-only
// call, a value-only call, a variadic call, a result-less call — plus a
// cross-package one, and the minimum of a project the harness imports
// (pkg/middleware, the service's connect package).
func programmableStubFixture(t *testing.T, forgeRoot string) string {
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
		"gen/services/chats/v1/chatsv1connect/chats.connect.go": `package chatsv1connect

import "connectrpc.com/connect"

type ChatsServiceClient interface{}

func NewChatsServiceClient(connect.HTTPClient, string, ...connect.ClientOption) ChatsServiceClient {
	return nil
}
`,
		"internal/policy/policy.go": `package policy

import "context"

type Service interface {
	Allowed(ctx context.Context, userID string) (bool, error)
}
`,
		"internal/handlers/chats/service.go": `package chats

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"example.com/proj/internal/policy"
)

type Chat struct{ ID, UserID string }

// Repository's parameter names are ones a stub body must not reuse (the
// receiver, testkit); the stub renames every parameter, so they cannot leak.
type Repository interface {
	GetChat(ctx context.Context, s string) (*Chat, error)
	Resolve(ctx context.Context, testkit string) error
	Count(ctx context.Context) int
	Tag(ctx context.Context, id string, tags ...string) error
	Close()
}

type Deps struct {
	Logger *slog.Logger
	Repo   Repository
	Policy policy.Service
}

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) {
	if deps.Repo == nil || deps.Policy == nil {
		return nil, errors.New("chats: Repo and Policy are required")
	}
	return &Service{deps: deps}, nil
}

func (s *Service) Register(*http.ServeMux, ...connect.HandlerOption) {}

// Owner is a handler-shaped read: it trusts a nil error to mean found.
func (s *Service) Owner(ctx context.Context, id string) (string, error) {
	chat, err := s.deps.Repo.GetChat(ctx, id)
	if err != nil {
		return "", err
	}
	return chat.UserID, nil
}
`,
		// What a project's own external test writes against the harness.
		"internal/handlers/chats/stub_use_test.go": `package chats_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/proj/internal/handlers/chats"
)

var errNotFound = errors.New("chat not found")

func TestUnprogrammedLookupRefusesInsteadOfAnsweringFound(t *testing.T) {
	svc := chats.NewTestChats(t)
	_, err := svc.Owner(context.Background(), "c1")
	if err == nil {
		t.Fatal("an unprogrammed lookup answered as if found")
	}
	if !strings.Contains(err.Error(), "StubRepo.GetChatFunc not set") {
		t.Fatalf("the refusal must name the stub and the Func to set, got: %v", err)
	}
}

func TestProgrammedLookupReachesTheNotFoundBranch(t *testing.T) {
	repo := &chats.StubRepo{
		GetChatFunc: func(_ context.Context, id string) (*chats.Chat, error) {
			if id == "c1" {
				return &chats.Chat{ID: id, UserID: "u1"}, nil
			}
			return nil, errNotFound
		},
	}
	svc := chats.NewTestChats(t, chats.WithChatsDeps(chats.Deps{Repo: repo}))
	if owner, err := svc.Owner(context.Background(), "c1"); err != nil || owner != "u1" {
		t.Fatalf("Owner(c1) = %q, %v; want u1, nil", owner, err)
	}
	if _, err := svc.Owner(context.Background(), "missing"); !errors.Is(err, errNotFound) {
		t.Fatalf("Owner(missing) err = %v, want the programmed not-found", err)
	}
	if n := repo.CallCount("GetChat"); n != 2 {
		t.Fatalf("Recorder saw %d GetChat calls, want 2", n)
	}
}

func TestEveryErrorReturningMethodRefusesUnprogrammed(t *testing.T) {
	repo := &chats.StubRepo{}
	ctx := context.Background()
	if err := repo.Resolve(ctx, "q1"); err == nil {
		t.Error("error-only Resolve answered nil (ALLOW) unprogrammed")
	}
	if err := repo.Tag(ctx, "c1", "a", "b"); err == nil {
		t.Error("variadic Tag answered nil unprogrammed")
	}
	if n := repo.Count(ctx); n != 0 {
		t.Errorf("value-only Count = %d unprogrammed, want its zero", n)
	}
	repo.Close() // no results: a no-op, never a panic

	pol := &chats.StubPolicy{}
	if ok, err := pol.Allowed(ctx, "u1"); err == nil || ok {
		t.Errorf("cross-package Allowed = %v, %v unprogrammed; want false and a refusal", ok, err)
	}
}

func TestVariadicArgumentsAreForwarded(t *testing.T) {
	var got []string
	repo := &chats.StubRepo{TagFunc: func(_ context.Context, _ string, tags ...string) error {
		got = tags
		return nil
	}}
	if err := repo.Tag(context.Background(), "c1", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("TagFunc received %v, want [a b]", got)
	}
}
`,
	})
}

// TestGenerateBootstrapTesting_StubsAreProgrammableAndRefuseUnprogrammed is
// the finding: through the generated harness, an unprogrammed lookup refuses
// with a named error instead of answering (nil, nil), a programmed one drives
// the handler into its not-found branch, and every error-returning method
// shape refuses until programmed. It runs the fixture's tests with `go test`,
// so the stub is checked as compiled code a project's test calls, not as text.
func TestGenerateBootstrapTesting_StubsAreProgrammableAndRefuseUnprogrammed(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs a fixture module with go test; runs in task test")
	}
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve forge root: %v", err)
	}
	root := programmableStubFixture(t, forgeRoot)
	helpers := readHarness(t, root, "internal/handlers/chats",
		[]ServiceDef{{Name: "ChatsService", ModulePath: "example.com/proj"}}, nil)

	cmd := exec.Command("go", "test", "-count=1", "./internal/handlers/chats/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, terr := cmd.CombinedOutput(); terr != nil {
		t.Fatalf("tests against the generated stubs failed: %v\n%s\n---- %s ----\n%s",
			terr, out, componentTestHelperFile, helpers)
	}
}

// TestComputeAutoStubs_StubRendering pins the rendered pieces the template
// assembles, including the names chosen to dodge what the body spells.
func TestComputeAutoStubs_StubRendering(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/chats", map[string]string{
		"internal/handlers/chats/service.go": `package chats

import (
	"context"
	"log/slog"
)

type Chat struct{}

type Repository interface {
	GetChat(ctx context.Context, s string) (*Chat, error)
	Tag(ctx context.Context, tags ...string) error
	Count() int
}

type Deps struct {
	Logger *slog.Logger
	Repo   Repository
}
`,
	})
	stubs, _ := computeAutoStubs(handlerDir, "")
	if len(stubs) != 1 {
		t.Fatalf("want one auto-stub, got %d: %+v", len(stubs), stubs)
	}
	stub := stubs[0]
	if stub.StubType != "StubRepo" {
		t.Errorf("StubType = %q, want the exported StubRepo the external test package can name", stub.StubType)
	}
	byName := methodsByName(t, stub)

	get := byName["GetChat"]
	for field, want := range map[string]string{
		"ReturnStatement": `return nil, testkit.StubNotConfigured("StubRepo", "GetChat")`,
		"NamedParams":     "a0 context.Context, a1 string",
		"CallArgs":        "a0, a1",
		"FuncType":        "func(context.Context, string) (*Chat, error)",
	} {
		got := map[string]string{
			"ReturnStatement": get.ReturnStatement, "NamedParams": get.NamedParams,
			"CallArgs": get.CallArgs, "FuncType": get.FuncType,
		}[field]
		if got != want {
			t.Errorf("GetChat.%s = %q, want %q", field, got, want)
		}
	}

	tag := byName["Tag"]
	if tag.CallArgs != "a0, a1..." || tag.FuncType != "func(context.Context, ...string) error" {
		t.Errorf("variadic Tag rendered CallArgs=%q FuncType=%q", tag.CallArgs, tag.FuncType)
	}
	if got := byName["Count"].ReturnStatement; got != "return 0" {
		t.Errorf("value-only Count must keep its zero (no error to refuse on), got %q", got)
	}
}
