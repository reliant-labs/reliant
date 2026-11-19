// File: internal/cli/scaffold/rpc_test.go
//
// Tests for `forge scaffold rpc <svc> <Name> [--stream M]`. They exercise:
//
//   - happy paths for each stream mode (unary/server/client/bidi)
//     produce the canonical Connect signature shape — locked in so a
//     codegen refactor that touches buildMethodSignature doesn't quietly
//     drift the scaffold against generated code.
//   - proto snippet places `stream` in the right slot for each mode.
//   - FS preconditions: handler dir must exist, target file must not.
//   - parseStreamMode accepts the documented aliases and rejects others.
//   - subcommand is registered on `forge scaffold`.

package scaffold

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
)

func TestParseStreamMode(t *testing.T) {
	cases := []struct {
		in      string
		want    rpcStreamMode
		wantErr bool
	}{
		{"", rpcUnary, false},
		{"unary", rpcUnary, false},
		{"server", rpcServerStream, false},
		{"SERVER", rpcServerStream, false}, // case-insensitive
		{"client", rpcClientStream, false},
		{"bidi", rpcBidiStream, false},
		{"bidirectional", rpcBidiStream, false},
		{"both", rpcBidiStream, false},
		{"  server  ", rpcServerStream, false}, // trim whitespace
		{"nonsense", 0, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			got, err := parseStreamMode(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("parseStreamMode(%q) expected error, got nil", c.in)
				}
				return
			}
			if err != nil {
				t.Errorf("parseStreamMode(%q) unexpected error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseStreamMode(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestBuildRPCHandlerStub_SignaturesPerMode pins the Connect signature
// for each stream mode against the shapes codegen emits in
// internal/codegen/generator.go's buildMethodSignature. If codegen
// ever changes those, this test surfaces the divergence — the scaffold
// must match what `forge generate` produces or hand-written RPCs
// silently fail to satisfy the generated Service interface.
func TestBuildRPCHandlerStub_SignaturesPerMode(t *testing.T) {
	cases := []struct {
		name string
		mode rpcStreamMode
		want string // substring that uniquely identifies the signature shape
	}{
		{
			name: "unary",
			mode: rpcUnary,
			want: "func (s *Service) Foo(ctx context.Context, req *connect.Request[pb.FooRequest]) (*connect.Response[pb.FooResponse], error)",
		},
		{
			name: "server-stream",
			mode: rpcServerStream,
			want: "func (s *Service) Foo(ctx context.Context, req *connect.Request[pb.FooRequest], stream *connect.ServerStream[pb.FooResponse]) error",
		},
		{
			name: "client-stream",
			mode: rpcClientStream,
			want: "func (s *Service) Foo(ctx context.Context, stream *connect.ClientStream[pb.FooRequest]) (*connect.Response[pb.FooResponse], error)",
		},
		{
			name: "bidi-stream",
			mode: rpcBidiStream,
			want: "func (s *Service) Foo(ctx context.Context, stream *connect.BidiStream[pb.FooRequest, pb.FooResponse]) error",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := buildRPCHandlerStub("billing", "Foo", "example.com/app/gen/services/billing/v1", c.mode)
			if !strings.Contains(got, c.want) {
				t.Errorf("stub for %s missing canonical signature\nwant substring:\n%s\nfull stub:\n%s", c.name, c.want, got)
			}
			if !strings.HasPrefix(got, "package billing\n") {
				t.Errorf("stub must declare `package billing`, got prefix:\n%s", got[:40])
			}
			// ScaffoldStub, not Unimplemented: the scaffold test row
			// keys on a sentinel only forge's own untouched stub can
			// emit, so implementing the RPC turns the row red however
			// the implementation fails. A bare Unimplemented here would
			// be indistinguishable from a finished handler's nil-guard.
			if !strings.Contains(got, "svcerr.ScaffoldStub(") {
				t.Errorf("stub must return ScaffoldStub (routed through svcerr) before the impl lands, got:\n%s", got)
			}
		})
	}
}

// TestBuildRPCProtoSnippet_StreamKeywordPlacement pins where the
// `stream` keyword appears in the proto rpc line per Connect/buf's
// expected shape. A bad position here would leave the user pasting
// proto that fails buf lint.
func TestBuildRPCProtoSnippet_StreamKeywordPlacement(t *testing.T) {
	cases := []struct {
		name string
		mode rpcStreamMode
		want string
	}{
		{"unary", rpcUnary, "rpc Foo(FooRequest) returns (FooResponse);"},
		{"server", rpcServerStream, "rpc Foo(FooRequest) returns (stream FooResponse);"},
		{"client", rpcClientStream, "rpc Foo(stream FooRequest) returns (FooResponse);"},
		{"bidi", rpcBidiStream, "rpc Foo(stream FooRequest) returns (stream FooResponse);"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := buildRPCProtoSnippet("Foo", c.mode)
			if !strings.Contains(got, c.want) {
				t.Errorf("proto snippet for %s missing rpc line\nwant:\n%s\nfull snippet:\n%s", c.name, c.want, got)
			}
			if !strings.Contains(got, "message FooRequest {") || !strings.Contains(got, "message FooResponse {") {
				t.Errorf("proto snippet missing message stubs:\n%s", got)
			}
		})
	}
}

func TestRunAddRPC_HappyPath(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "tasks")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handlers/tasks: %v", err)
	}

	if err := runRPC(testFactory(), "tasks", "TailEvents", rpcServerStream); err != nil {
		t.Fatalf("runRPC: %v", err)
	}

	// rpc_ prefix + snake_case filename keeps the per-RPC files
	// visually grouped in directory listings.
	want := filepath.Join(handlerDir, "rpc_tail_events.go")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read scaffolded file: %v", err)
	}
	content := string(body)
	if !strings.Contains(content, "func (s *Service) TailEvents(") {
		t.Errorf("scaffolded file missing handler signature, got:\n%s", content)
	}
	if !strings.Contains(content, "ServerStream[pb.TailEventsResponse]") {
		t.Errorf("server-stream mode should produce a pb-qualified ServerStream signature, got:\n%s", content)
	}
	// The file must be buildable as written: a resolved import, not a
	// commented placeholder the author is expected to repair.
	if strings.Contains(content, "<your-module>") {
		t.Errorf("scaffolded file still carries an unresolved import placeholder, got:\n%s", content)
	}
	if !strings.Contains(content, "\tpb \"") {
		t.Errorf("scaffolded file must import the generated proto package for real, got:\n%s", content)
	}
}

func TestRunAddRPC_RejectsLowercaseStart(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "handlers", "tasks"), 0o755); err != nil {
		t.Fatalf("mkdir handlers/tasks: %v", err)
	}

	// Connect RPC method names must be exported — start uppercase.
	// validateIdentifier accepts lowercase starts (it's also used for
	// filenames), so we have a tighter check just for RPC names.
	err := runRPC(testFactory(), "tasks", "tailEvents", rpcUnary)
	if err == nil {
		t.Fatal("expected error for lowercase RPC name, got nil")
	}
	if !strings.Contains(err.Error(), "uppercase") {
		t.Errorf("error should mention uppercase requirement, got: %v", err)
	}
}

func TestRunAddRPC_MissingHandlerDir(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	// No handlers/tasks dir on disk.

	err := runRPC(testFactory(), "tasks", "ListByOwner", rpcUnary)
	if err == nil {
		t.Fatal("expected error when handler directory missing, got nil")
	}
	if !strings.Contains(err.Error(), "no handler directory") {
		t.Errorf("error should explain missing dir, got: %v", err)
	}
}

func TestRunAddRPC_FileAlreadyExists(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "tasks")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handlers/tasks: %v", err)
	}
	existing := filepath.Join(handlerDir, "rpc_list_by_owner.go")
	if err := os.WriteFile(existing, []byte("package tasks\n// user-edited\n"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	err := runRPC(testFactory(), "tasks", "ListByOwner", rpcUnary)
	if err == nil {
		t.Fatal("expected error when target file exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should explain conflict, got: %v", err)
	}
	// The user-edited file must not have been clobbered.
	body, _ := os.ReadFile(existing)
	if !strings.Contains(string(body), "user-edited") {
		t.Error("existing file was overwritten; refusal path is broken")
	}
}

// TestRunAddRPC_NotInProtoKeepsSnippetBehavior pins the "RPC not in the
// proto yet" path: a correctly-signed bare handler stub is written and NO
// domain package is created (the pb-through world has no separate domain
// vertical).
func TestRunAddRPC_NotInProtoKeepsSnippetBehavior(t *testing.T) {
	dir := setupVerticalProject(t) // handlers + descriptor, but no tasks.proto on disk

	if err := runRPC(testFactory(), "tasks", "BrandNewThing", rpcUnary); err != nil {
		t.Fatalf("runRPC: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "internal", "handlers", "tasks", "rpc_brand_new_thing.go"))
	if err != nil {
		t.Fatalf("bare stub should be written for a not-in-proto RPC: %v", err)
	}
	if !strings.Contains(string(body), "svcerr.ScaffoldStub(") {
		t.Errorf("bare stub should return ScaffoldStub via svcerr:\n%s", body)
	}
	// No domain package: the pb-through collapse removed the RPC vertical.
	if _, statErr := os.Stat(filepath.Join(dir, "internal", "tasks")); !os.IsNotExist(statErr) {
		t.Error("not-in-proto RPC must not create a domain package")
	}
}

// TestRunAddRPC_StubIsScannableAsUnwired is the behavioural half of the
// unwired-stub marker contract at THIS emitter: whatever `forge scaffold rpc`
// writes must be findable by codegen.ScanUnwiredStubMethods — the canonical
// reader that `forge project audit`, CRUD gen's stub→shim excision, and
// out-of-tree orchestrators all key on.
//
// It asserts through the reader rather than against a substring, and it runs
// the real command for all four stream modes, because the shape is built per
// mode: a marker stamped on the unary branch alone would still satisfy any
// single-shape check.
//
// Without the fix this is red four times over. The command emitted the same
// placeholder handler the generate pipeline emits and stamped no marker, so a
// project whose custom RPCs were added this way reported zero unwired stubs
// while every one of them returned Unimplemented.
func TestRunAddRPC_StubIsScannableAsUnwired(t *testing.T) {
	for _, c := range []struct {
		name string
		rpc  string
		mode rpcStreamMode
	}{
		{"unary", "ListByOwner", rpcUnary},
		{"server-stream", "TailEvents", rpcServerStream},
		{"client-stream", "UploadBatch", rpcClientStream},
		{"bidi-stream", "Chat", rpcBidiStream},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			dir := withTempProject(t, minimalServiceForgeYAML)
			markServiceProject(t, dir)
			handlerDir := filepath.Join(dir, "internal", "handlers", "tasks")
			if err := os.MkdirAll(handlerDir, 0o755); err != nil {
				t.Fatalf("mkdir handlers/tasks: %v", err)
			}

			if err := runRPC(testFactory(), "tasks", c.rpc, c.mode); err != nil {
				t.Fatalf("runRPC: %v", err)
			}

			got := codegen.ScanUnwiredStubMethods(handlerDir)
			if !got[c.rpc] {
				body, _ := os.ReadFile(filepath.Join(handlerDir, "rpc_"+naming.ToSnakeCase(c.rpc)+".go"))
				t.Fatalf("the scaffolded %s stub carries no %q marker, so nothing that reads the tree "+
					"can tell it apart from an implemented handler (scanner found: %v)\n%s",
					c.rpc, codegen.UnwiredStubMarkerPrefix, got, body)
			}
			// The symbol is attributed to the handler package, not left bare:
			// `forge project audit` reports <pkg>.<Method>.
			body, err := os.ReadFile(filepath.Join(handlerDir, "rpc_"+naming.ToSnakeCase(c.rpc)+".go"))
			if err != nil {
				t.Fatalf("read scaffolded file: %v", err)
			}
			if want := codegen.UnwiredStubMarkerComment("tasks", c.rpc); !strings.Contains(string(body), want) {
				t.Errorf("marker should name the handler package, want %q:\n%s", want, body)
			}
		})
	}
}

// TestBuildRPCHandlerStub_MarkerIsTheLastDocLine pins the marker's POSITION.
// revive's `exported` rule (which forge's own lint enables) requires the first
// doc line to start with the method name, so a marker that leads makes forge
// lint reject a file forge just scaffolded. The templates put it last for the
// same reason.
func TestBuildRPCHandlerStub_MarkerIsTheLastDocLine(t *testing.T) {
	for _, mode := range []rpcStreamMode{rpcUnary, rpcServerStream, rpcClientStream, rpcBidiStream} {
		got := buildRPCHandlerStub("billing", "Foo", "example.com/app/gen/services/billing/v1", mode)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "rpc_foo.go", got, parser.ParseComments)
		if err != nil {
			t.Fatalf("mode %v: emitted stub does not parse: %v\n%s", mode, err, got)
		}
		var fn *ast.FuncDecl
		for _, d := range file.Decls {
			if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "Foo" {
				fn = f
			}
		}
		if fn == nil || fn.Doc == nil {
			t.Fatalf("mode %v: no documented Foo method in the emitted stub:\n%s", mode, got)
		}
		lines := fn.Doc.List
		if !strings.HasPrefix(lines[0].Text, "// Foo ") {
			t.Errorf("mode %v: marker must not displace the revive-required first line, got %q", mode, lines[0].Text)
		}
		if last := lines[len(lines)-1].Text; !codegen.UnwiredStubMarkerRE.MatchString(last) {
			t.Errorf("mode %v: the marker must be the LAST doc line, got %q", mode, last)
		}
	}
}

func TestAddRPCSubcommandRegistered(t *testing.T) {
	root := newScaffoldCmd(testFactory())
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "rpc" {
			found = true
			if sub.Args == nil {
				t.Error("rpc subcommand should declare an Args validator")
			}
			if sub.Flag("stream") == nil {
				t.Error("rpc subcommand should expose --stream flag")
			}
			// The vertical-only flags were removed in the pb-through collapse.
			for _, gone := range []string{"interactor", "all-unwired", "service"} {
				if sub.Flag(gone) != nil {
					t.Errorf("rpc subcommand should NOT expose the removed --%s flag", gone)
				}
			}
			break
		}
	}
	if !found {
		t.Fatal("rpc subcommand not registered on `forge scaffold`")
	}
	if !strings.Contains(root.Long, "rpc") {
		t.Error("`forge scaffold --help` Long string should advertise the rpc subcommand")
	}
}

// TestBuildRPCHandlerStub_CompilesAsWritten pins the property that
// actually matters: the file `forge scaffold rpc` writes must BUILD.
//
// It used to ship the generated-proto import as a COMMENT —
// `// billingpb "<your-module>/gen/services/<service>/v1"` — with the
// request/response types left unqualified, on the stated theory that "the
// user will fix the placeholder when the file fails to compile". The
// charter names this command as the way to add a custom RPC, so the
// documented path produced ten `undefined: XRequest` errors and broke
// `forge generate` before the author had written a line.
//
// forge.yaml carries module_path and the service name is the command's own
// argument, so every piece of that import is derivable.
func TestBuildRPCHandlerStub_CompilesAsWritten(t *testing.T) {
	const pbPath = "example.com/app/gen/services/billing/v1"
	got := buildRPCHandlerStub("billing", "Foo", pbPath, rpcUnary)

	if strings.Contains(got, "<your-module>") || strings.Contains(got, "<service>") {
		t.Errorf("stub still ships an unresolved import placeholder:\n%s", got)
	}
	if want := "\tpb \"" + pbPath + "\"\n"; !strings.Contains(got, want) {
		t.Errorf("stub must import the generated proto package as a REAL import\nwant: %q\ngot:\n%s", want, got)
	}
	// The types are declared in gen/, never in the handler package, so an
	// unqualified name cannot resolve.
	for _, unqualified := range []string{"[FooRequest]", "[FooResponse]"} {
		if strings.Contains(got, unqualified) {
			t.Errorf("stub references %s unqualified; it lives in the pb package:\n%s", unqualified, got)
		}
	}

	// Parse it: a stub that does not even parse cannot compile.
	if _, err := parser.ParseFile(token.NewFileSet(), "rpc_foo.go", got, parser.AllErrors); err != nil {
		t.Fatalf("emitted stub does not parse: %v\n%s", err, got)
	}
}

// The stub compiles (above) — it must also LINT. forge's own golangci
// config enables revive's `exported` rule, which requires an exported
// method to carry a doc comment whose first word is the method name. The
// stub's explanatory block used to sit between `package` and `import`,
// where it is a floating comment and not documentation of anything: so
// `forge scaffold rpc` on a fresh project emitted the one file that made
// `forge lint` fail, and the gate the charter runs is
// `generate && lint && build`.
func TestBuildRPCHandlerStub_ExportedMethodIsDocumented(t *testing.T) {
	for _, mode := range []rpcStreamMode{rpcUnary, rpcServerStream, rpcClientStream, rpcBidiStream} {
		got := buildRPCHandlerStub("billing", "Foo", "example.com/app/gen/services/billing/v1", mode)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "rpc_foo.go", got, parser.ParseComments)
		if err != nil {
			t.Fatalf("mode %v: emitted stub does not parse: %v\n%s", mode, err, got)
		}

		var fn *ast.FuncDecl
		for _, d := range file.Decls {
			if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "Foo" {
				fn = f
			}
		}
		if fn == nil {
			t.Fatalf("mode %v: no Foo method in the emitted stub:\n%s", mode, got)
		}
		if fn.Doc == nil {
			t.Errorf("mode %v: exported method Foo has no doc comment — revive's `exported` rule fails "+
				"forge lint on a file forge itself just scaffolded:\n%s", mode, got)
			continue
		}
		if first := fn.Doc.List[0].Text; !strings.HasPrefix(first, "// Foo ") {
			t.Errorf("mode %v: doc comment must start with the method name (revive `exported`), got %q", mode, first)
		}
	}
}
