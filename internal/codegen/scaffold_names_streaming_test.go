// File: internal/codegen/scaffold_names_streaming_test.go
//
// Three things every reliant P3b lane tripped over in what forge scaffolds:
// an auto-stub named stubTool_callRepo for a package spelled tool_call, a
// streaming rpc's scaffold test that was a TODO under the unary test's
// header, and a generate log naming <svc>_mock.go for a file written as
// <svc>_mock_gen.go.

package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/goexec"
)

// TestComputeAutoStubs_StubTypeIsGoNamed: an auto-stub is named
// Stub<Field> (the programmable contract-mock shape), so no part of the
// package name, underscore or not, ever reaches the identifier.
func TestComputeAutoStubs_StubTypeIsGoNamed(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/tool_call", map[string]string{
		"internal/handlers/tool_call/service.go": `package tool_call

import "log/slog"

type Repository interface{ Cancel(id string) error }

type Deps struct {
	Logger *slog.Logger
	Repo   Repository
}
`,
	})
	stubs, _ := computeAutoStubs(handlerDir, "")
	if len(stubs) != 1 {
		t.Fatalf("want one auto-stub, got %d", len(stubs))
	}
	if got := stubs[0].StubType; got != "StubRepo" {
		t.Errorf("StubType = %q, want StubRepo", got)
	}
}

// TestMockRelPath_IsTheFileGenerateMockWrites pins the one name the mock's
// writer and generate's progress line share.
func TestMockRelPath_IsTheFileGenerateMockWrites(t *testing.T) {
	projectDir := t.TempDir()
	checksums.ResetSkipWrite()
	t.Cleanup(checksums.ResetSkipWrite)
	svc := ServiceDef{
		Name:       "ToolCallService",
		Package:    "tool_call.v1",
		GoPackage:  "github.com/test/proj/gen/services/tool_call/v1",
		PkgName:    "tool_callv1",
		ModulePath: "github.com/test/proj",
		Methods:    []Method{{Name: "CancelToolCall", InputType: "CancelToolCallRequest", OutputType: "CancelToolCallResponse"}},
	}
	if written, err := GenerateMock(svc, projectDir, filepath.Join(projectDir, "internal", "handlers", "mocks"), nil); err != nil || !written.Wrote {
		t.Fatalf("GenerateMock = %v, %v", written, err)
	}
	rel := MockRelPath(svc)
	if rel != "internal/handlers/mocks/tool_call_mock_gen.go" {
		t.Errorf("MockRelPath = %q", rel)
	}
	if _, err := os.Stat(filepath.Join(projectDir, rel)); err != nil {
		t.Fatalf("GenerateMock did not write MockRelPath %s: %v", rel, err)
	}
}

// streamingFixture is a service with one rpc of each streaming kind, served
// through hand-written connect wiring (what protoc-gen-connect-go would
// emit) over well-known proto messages, so a real client can drive it.
func streamingFixture(t *testing.T, forgeRoot string) string {
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
		"gen/services/feeds/v1/pb.go": `package feedsv1

import "google.golang.org/protobuf/types/known/emptypb"

type TailRequest = emptypb.Empty
type TailResponse = emptypb.Empty
type UploadRequest = emptypb.Empty
type UploadResponse = emptypb.Empty
type ChatRequest = emptypb.Empty
type ChatResponse = emptypb.Empty
`,
		"gen/services/feeds/v1/feedsv1connect/feeds.connect.go": `package feedsv1connect

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	v1 "example.com/proj/gen/services/feeds/v1"
)

const (
	tailProcedure   = "/feeds.v1.FeedsService/Tail"
	uploadProcedure = "/feeds.v1.FeedsService/Upload"
)

type FeedsServiceClient interface {
	Tail(context.Context, *connect.Request[v1.TailRequest]) (*connect.ServerStreamForClient[v1.TailResponse], error)
	Upload(context.Context) *connect.ClientStreamForClient[v1.UploadRequest, v1.UploadResponse]
}

type client struct {
	tail   *connect.Client[v1.TailRequest, v1.TailResponse]
	upload *connect.Client[v1.UploadRequest, v1.UploadResponse]
}

func NewFeedsServiceClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) FeedsServiceClient {
	return &client{
		tail:   connect.NewClient[v1.TailRequest, v1.TailResponse](httpClient, baseURL+tailProcedure, opts...),
		upload: connect.NewClient[v1.UploadRequest, v1.UploadResponse](httpClient, baseURL+uploadProcedure, opts...),
	}
}

func (c *client) Tail(ctx context.Context, req *connect.Request[v1.TailRequest]) (*connect.ServerStreamForClient[v1.TailResponse], error) {
	return c.tail.CallServerStream(ctx, req)
}

func (c *client) Upload(ctx context.Context) *connect.ClientStreamForClient[v1.UploadRequest, v1.UploadResponse] {
	return c.upload.CallClientStream(ctx)
}

type Handler interface {
	Tail(context.Context, *connect.Request[v1.TailRequest], *connect.ServerStream[v1.TailResponse]) error
	Upload(context.Context, *connect.ClientStream[v1.UploadRequest]) (*connect.Response[v1.UploadResponse], error)
}

func Mount(mux *http.ServeMux, h Handler, opts ...connect.HandlerOption) {
	mux.Handle(tailProcedure, connect.NewServerStreamHandler(tailProcedure, h.Tail, opts...))
	mux.Handle(uploadProcedure, connect.NewClientStreamHandler(uploadProcedure, h.Upload, opts...))
}
`,
		// The handler is forge's own pb-through stub for each kind.
		"internal/handlers/feeds/service.go": `package feeds

import (
	"context"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "example.com/proj/gen/services/feeds/v1"
	"example.com/proj/gen/services/feeds/v1/feedsv1connect"
)

type Deps struct{ Logger *slog.Logger }

type Service struct{ deps Deps }

func New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }

func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	feedsv1connect.Mount(mux, s, opts...)
}

func (s *Service) Tail(ctx context.Context, req *connect.Request[pb.TailRequest], stream *connect.ServerStream[pb.TailResponse]) error {
	return svcerr.Wrap(svcerr.ScaffoldStub("Tail"))
}

func (s *Service) Upload(ctx context.Context, stream *connect.ClientStream[pb.UploadRequest]) (*connect.Response[pb.UploadResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("Upload"))
}
`,
	})
}

func feedsServiceDef() ServiceDef {
	return ServiceDef{
		Name:       "FeedsService",
		ModulePath: "example.com/proj",
		GoPackage:  "example.com/proj/gen/services/feeds/v1",
		PkgName:    "feedsv1",
		Methods: []Method{
			{Name: "Tail", InputType: "TailRequest", OutputType: "TailResponse", ServerStreaming: true},
			{Name: "Upload", InputType: "UploadRequest", OutputType: "UploadResponse", ClientStreaming: true},
			{Name: "Chat", InputType: "ChatRequest", OutputType: "ChatResponse", ClientStreaming: true, ServerStreaming: true},
		},
	}
}

// TestScaffoldTests_StreamingRPCsGetARunnableSkeleton is the finding: a
// server- or client-streaming rpc's scaffold test drives the stub through
// the harness's server and passes as written (red the moment the rpc is
// implemented); a bidi rpc's file says why it has no case, under its own
// header rather than the unary one.
func TestScaffoldTests_StreamingRPCsGetARunnableSkeleton(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs a fixture module with go test; runs in task test")
	}
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve forge root: %v", err)
	}
	root := streamingFixture(t, forgeRoot)
	svc := feedsServiceDef()
	readHarness(t, root, "internal/handlers/feeds", []ServiceDef{svc}, nil)
	handlerDir := filepath.Join(root, "internal", "handlers", "feeds")
	if err := writeScaffoldTests(handlerDir, mapServiceDefToTemplateData(svc, root)); err != nil {
		t.Fatalf("writeScaffoldTests: %v", err)
	}

	read := func(rpc string) string {
		b, rerr := os.ReadFile(filepath.Join(handlerDir, ScaffoldTestFileName(rpc)))
		if rerr != nil {
			t.Fatalf("read %s scaffold: %v", rpc, rerr)
		}
		return string(b)
	}
	for _, rpc := range []string{"Tail", "Upload", "Chat"} {
		if strings.Contains(read(rpc), "tdd.RunRPCCases`") {
			t.Errorf("%s is streaming but its scaffold carries the unary RunRPCCases header:\n%s", rpc, read(rpc))
		}
	}
	if chat := read("Chat"); !strings.Contains(chat, "BIDIRECTIONAL") || !strings.Contains(chat, "HTTP/2") {
		t.Errorf("the bidi scaffold must say why it has no case:\n%s", chat)
	}

	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "TestTail_Generated|TestUpload_Generated", "./internal/handlers/feeds/")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the streaming scaffold tests must pass against forge's stubs: %v\n%s\n---- Tail ----\n%s\n---- Upload ----\n%s",
			err, out, read("Tail"), read("Upload"))
	}
	for _, ran := range []string{"--- PASS: TestTail_Generated/unimplemented_scaffold", "--- PASS: TestUpload_Generated/unimplemented_scaffold"} {
		if !strings.Contains(string(out), ran) {
			t.Errorf("expected %q in the run:\n%s", ran, out)
		}
	}
}
