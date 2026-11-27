//go:build e2e

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	connectv1 "connectrpc.com/connect"
	"golang.org/x/mod/modfile"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EConnectV2ScaffoldServesRPC proves a connect-go v2 project end to
// end: `project new --connect v2` → CRUD entity plus custom unary,
// server-stream, client-stream and bidi rpcs → generate → build/vet/test →
// boot → real calls → generate again changes nothing.
//
// The generated tdd/component/middleware tests exercise v2 in `go test`. The
// booted server is then called four ways:
//
//   - a connect-go v2 client built from the project's OWN generated code (a
//     skip-unless-WIRE_BASE_URL test the project carries, so it can import
//     the project's gen module): unary, CRUD, server-stream, client-stream,
//     bidi (h2c), errors, auth;
//   - raw Connect JSON over HTTP POST — what a curl or a browser sends;
//   - a connect-go v1 client built in THIS process. The project's gen types
//     are not importable here, so the v1 generic client speaks to the same
//     routes with well-known wrapper messages that are wire-identical to the
//     project's single-scalar messages (StringValue ≙ {string x = 1},
//     Int32Value ≙ {int32 x = 1}): old clients keep working unchanged;
//   - the gRPC protocol and grpc.health.v1.Health/Check.
func TestE2EConnectV2ScaffoldServesRPC(t *testing.T) {
	sharedTestPostgres(t)
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "connv2", "--mod", "example.com/connv2", "--service", "orders", "--connect", "v2")
	projectDir := filepath.Join(dir, "connv2")
	addCorpusForgePkgReplace(t, projectDir)

	// ── Author the service: CRUD entity + every custom rpc shape ────────
	protoPath := filepath.Join(projectDir, "proto", "services", "orders", "v1", "orders.proto")
	proto := readFileE2E(t, protoPath)
	const todo = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(proto, todo) {
		t.Fatalf("scaffolded proto changed shape:\n%s", proto)
	}
	proto = strings.Replace(proto, todo, `  rpc Ping(PingRequest) returns (PingResponse);
  rpc Echo(EchoRequest) returns (EchoResponse);
  rpc PublicPing(PublicPingRequest) returns (PublicPingResponse) {
    option (forge.v1.method) = {auth_required: false};
  }
  rpc Fail(FailRequest) returns (FailResponse);
  rpc Watch(WatchRequest) returns (stream WatchResponse);
  rpc Upload(stream UploadRequest) returns (UploadResponse);
  rpc Chat(stream ChatRequest) returns (stream ChatResponse);
`, 1)
	proto += `
message PingRequest { string text = 1; }
message PingResponse { string text = 1; }
message EchoRequest { string text = 1; }
message EchoResponse { string text = 1; }
message PublicPingRequest { string text = 1; }
message PublicPingResponse { string text = 1; }
message FailRequest { string id = 1; }
message FailResponse {}
message WatchRequest { int32 count = 1; }
message WatchResponse { int32 seq = 1; }
message UploadRequest { int32 n = 1; }
message UploadResponse { int32 sum = 1; }
message ChatRequest { string text = 1; }
message ChatResponse { string text = 1; }

// forge:entity
message Order { string id = 1; string name = 2; }
`
	writeFileE2E(t, protoPath, proto)
	runCmd(t, projectDir, forgeBin, "scaffold") // births the entity table + CRUD, stubs every custom rpc

	// The normal one-rpc-at-a-time flow: declare Hello in the proto, then
	// `forge scaffold rpc` writes its stub in a v2 project.
	proto = readFileE2E(t, protoPath)
	proto = strings.Replace(proto, "  rpc Echo(EchoRequest) returns (EchoResponse);\n",
		"  rpc Echo(EchoRequest) returns (EchoResponse);\n  rpc Hello(HelloRequest) returns (HelloResponse);\n", 1)
	proto += "\nmessage HelloRequest { string name = 1; }\nmessage HelloResponse { string greeting = 1; }\n"
	writeFileE2E(t, protoPath, proto)
	runCmd(t, projectDir, forgeBin, "scaffold", "rpc", "orders", "Hello")

	ordersDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	// The v2 stubs carry the v2 shapes: bare messages, a named stream type.
	for file, want := range map[string]string{
		"rpc_ping.go":   "req *pb.PingRequest,\n) (*pb.PingResponse, error)",
		"rpc_hello.go":  "req *pb.HelloRequest,\n) (*pb.HelloResponse, error)",
		"rpc_watch.go":  "stream ordersv1connect.OrdersServiceWatchServerStream,\n) error",
		"rpc_upload.go": "stream ordersv1connect.OrdersServiceUploadServerStream,\n) (*pb.UploadResponse, error)",
		"rpc_chat.go":   "stream ordersv1connect.OrdersServiceChatServerStream,\n) error",
	} {
		if src := readFileE2E(t, filepath.Join(ordersDir, file)); !strings.Contains(src, want) {
			t.Errorf("%s is not a v2 stub (want %q):\n%s", file, want, src)
		}
	}
	crudShim := readFileE2E(t, filepath.Join(ordersDir, "handlers_crud.go"))
	for _, want := range []string{"crud.Create(", "crud.Get("} {
		if !strings.Contains(crudShim, want) {
			t.Errorf("handlers_crud.go should delegate through %s…:\n%s", want, crudShim)
		}
	}

	// ── Implement trivial bodies ────────────────────────────────────────
	writeConnV2Handlers(t, ordersDir)
	writeFileE2E(t, filepath.Join(projectDir, "internal", "wirecheck", "wire_test.go"), connV2WireCheckTest)

	runCmd(t, projectDir, forgeBin, "generate")

	// ── go.mod and imports select connect-go v2, and only v2 ────────────
	assertConnectV2Only(t, projectDir)

	// ── build, vet, and the generated unit/component/middleware tests ───
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	runCmd(t, projectDir, "go", "test", "-short", "./...")

	// ── Boot and call ───────────────────────────────────────────────────
	port := freePortE2E(t)
	serverBin := filepath.Join(projectDir, "connv2-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)
	pubPEM, bearer := mintDevJWT(t)

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		"JWT_SECRET="+pubPEM,
	)
	serverOut := &apiKeyServerLog{}
	cmd.Stdout = serverOut
	cmd.Stderr = serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	defer stop()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServer(t, base+"/healthz", 30*time.Second) {
		t.Fatalf("server did not answer /healthz\nserver output:\n%s", serverOut.String())
	}
	if !waitForServing(t, base, 30*time.Second) {
		t.Fatalf("server never became ready\nserver output:\n%s", serverOut.String())
	}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format+"\nserver output:\n%s", append(args, serverOut.String())...)
	}

	// /healthz and grpc.health.v1.Health/Check, no credentials.
	if status, _ := httpGetStatus(t, base+"/healthz"); status != http.StatusOK {
		fail("/healthz = %d, want 200", status)
	}
	status, body := postJSON(t, base+"/grpc.health.v1.Health/Check", "", "{}")
	if status != http.StatusOK || !strings.Contains(string(body), `"SERVING"`) {
		fail("grpc.health.v1.Health/Check = %d %s, want SERVING", status, body)
	}

	// ── Raw Connect JSON: what curl, a browser and any old client send ──
	rpc := func(name string) string { return base + "/services.orders.v1.OrdersService/" + name }

	status, body = postJSON(t, rpc("CreateOrder"), bearer, `{"name":"gizmo"}`)
	var created struct {
		Order struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"order"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &created) != nil || created.Order.ID == "" {
		t.Fatalf("raw JSON CreateOrder = %d %s, want a row\nserver output:\n%s", status, body, serverOut.String())
	}
	status, body = postJSON(t, rpc("GetOrder"), bearer, fmt.Sprintf(`{"id":%q}`, created.Order.ID))
	if status != http.StatusOK || !strings.Contains(string(body), `"name":"gizmo"`) {
		fail("raw JSON GetOrder = %d %s, want the created order", status, body)
	}
	status, body = postJSON(t, rpc("Ping"), bearer, `{"text":"hi"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"text":"pong:hi"`) {
		fail("raw JSON Ping = %d %s, want pong:hi", status, body)
	}
	status, body = postJSON(t, rpc("Hello"), bearer, `{"name":"forge"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"greeting":"hello forge"`) {
		fail("raw JSON Hello (scaffold rpc) = %d %s, want hello forge", status, body)
	}
	// An error keeps its code and message on the wire, as v1 sent them.
	status, body = postJSON(t, rpc("Fail"), bearer, `{"id":"o-1"}`)
	var wireErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if status != http.StatusNotFound || json.Unmarshal(body, &wireErr) != nil ||
		wireErr.Code != "not_found" || !strings.Contains(wireErr.Message, "order not found") {
		fail("raw JSON Fail = %d %s, want 404 {code:not_found, message ~ \"order not found\"}", status, body)
	}
	// auth_required (the default) answers unauthenticated without a token,
	// and a bad token is refused rather than downgraded to anonymous.
	for name, token := range map[string]string{"no token": "", "malformed token": "Bearer not-a-jwt"} {
		status, body = postJSON(t, rpc("Ping"), token, `{"text":"hi"}`)
		if status != http.StatusUnauthorized || !strings.Contains(string(body), `"unauthenticated"`) {
			fail("Ping with %s = %d %s, want 401 unauthenticated", name, status, body)
		}
	}
	// auth_required: false is public.
	status, body = postJSON(t, rpc("PublicPing"), "", `{"text":"anyone"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"text":"public:anyone"`) {
		fail("raw JSON PublicPing without a token = %d %s, want 200 public:anyone", status, body)
	}

	// ── A connect-go v1 client (old clients keep working) ───────────────
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h1 := &http.Client{}
	h2c := &http.Client{Transport: &http.Transport{Protocols: unencryptedHTTP2Protocols()}}
	withAuth := func(h http.Header) { h.Set("Authorization", bearer) }

	for _, tc := range []struct {
		name string
		hc   *http.Client
		opts []connectv1.ClientOption
	}{
		{"connect protobuf over HTTP/1.1", h1, nil},
		{"grpc over h2c", h2c, []connectv1.ClientOption{connectv1.WithGRPC()}},
	} {
		ping := connectv1.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](tc.hc, rpc("Ping"), tc.opts...)
		req := connectv1.NewRequest(wrapperspb.String("old"))
		withAuth(req.Header())
		resp, err := ping.CallUnary(ctx, req)
		if err != nil || resp.Msg.GetValue() != "pong:old" {
			fail("v1 client (%s) Ping = %v, %v; want pong:old", tc.name, resp, err)
		}

		noToken := connectv1.NewRequest(wrapperspb.String("old"))
		if _, err := ping.CallUnary(ctx, noToken); connectv1.CodeOf(err) != connectv1.CodeUnauthenticated {
			fail("v1 client (%s) Ping without a token = %v, want unauthenticated", tc.name, err)
		}

		failCall := connectv1.NewClient[wrapperspb.StringValue, emptypb.Empty](tc.hc, rpc("Fail"), tc.opts...)
		freq := connectv1.NewRequest(wrapperspb.String("o-1"))
		withAuth(freq.Header())
		_, err = failCall.CallUnary(ctx, freq)
		var cerr *connectv1.Error
		if !errors.As(err, &cerr) || cerr.Code() != connectv1.CodeNotFound || !strings.Contains(cerr.Message(), "order not found") {
			fail("v1 client (%s) Fail = %v, want not_found \"order not found\"", tc.name, err)
		}

		watch := connectv1.NewClient[wrapperspb.Int32Value, wrapperspb.Int32Value](tc.hc, rpc("Watch"), tc.opts...)
		wreq := connectv1.NewRequest(wrapperspb.Int32(3))
		withAuth(wreq.Header())
		stream, err := watch.CallServerStream(ctx, wreq)
		if err != nil {
			fail("v1 client (%s) Watch: %v", tc.name, err)
			continue
		}
		var seqs []int32
		for stream.Receive() {
			seqs = append(seqs, stream.Msg().GetValue())
		}
		if err := stream.Err(); err != nil || fmt.Sprint(seqs) != "[1 2 3]" {
			fail("v1 client (%s) Watch frames = %v, err = %v; want [1 2 3] then a clean end", tc.name, seqs, err)
		}
		_ = stream.Close()
	}

	// ── A connect-go v2 client built from the project's generated code ──
	wire := exec.Command("go", "test", "-count=1", "-v", "-run", "^TestWire", "./internal/wirecheck/")
	wire.Dir = projectDir
	wire.Env = goexec.WithTrimpath(append(os.Environ(),
		"WIRE_BASE_URL="+base,
		"WIRE_TOKEN="+bearer,
		"GOFLAGS=",
	))
	if out, err := wire.CombinedOutput(); err != nil {
		fail("v2 client wire test failed: %v\n%s", err, out)
	} else if !strings.Contains(string(out), "--- PASS: TestWire_V2Client") {
		fail("v2 client wire test did not run:\n%s", out)
	}

	stop()

	// ── generate is a fixed point ───────────────────────────────────────
	before := hashProjectTree(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")
	if diff := diffTreeE2E(before, hashProjectTree(t, projectDir)); diff != "" {
		t.Errorf("re-running forge generate changed the project:\n%s", diff)
	}
	runCmd(t, projectDir, "go", "build", "./...")
}

// assertConnectV2Only checks that go.mod requires connect-go v2 directly and
// connect-go v1 not at all directly, and that no project source imports v1.
func assertConnectV2Only(t *testing.T, projectDir string) {
	t.Helper()
	for _, rel := range []string{"go.mod", filepath.Join("gen", "go.mod")} {
		path := filepath.Join(projectDir, rel)
		mf, err := modfile.Parse(path, []byte(readFileE2E(t, path)), nil)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		var v2Direct bool
		for _, r := range mf.Require {
			switch {
			case r.Mod.Path == "connectrpc.com/connect/v2" && !r.Indirect:
				v2Direct = true
			case r.Mod.Path == "connectrpc.com/connect" && !r.Indirect:
				t.Errorf("%s requires connectrpc.com/connect (v1) directly: %s", rel, r.Mod.Version)
			}
		}
		if !v2Direct {
			t.Errorf("%s does not require connectrpc.com/connect/v2 directly", rel)
		}
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if corpusSkipDir(d.Name()) || d.Name() == ".forge-pkg" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // a broken file fails the build step with a better message
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "connectrpc.com/connect" || strings.HasPrefix(p, "connectrpc.com/connect/") && !strings.HasPrefix(p, "connectrpc.com/connect/v2") {
				rel, _ := filepath.Rel(projectDir, path)
				t.Errorf("%s imports connect-go v1 (%s)", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan project imports: %v", err)
	}
}

func writeConnV2Handlers(t *testing.T, ordersDir string) {
	t.Helper()
	write := func(name, body string) {
		t.Helper()
		writeFileE2E(t, filepath.Join(ordersDir, name), body)
		// The scaffolded row asserts the stub's Unimplemented answer and
		// self-destructs once the handler is real.
		stem := strings.TrimSuffix(strings.TrimPrefix(name, "rpc_"), ".go")
		_ = os.Remove(filepath.Join(ordersDir, "handlers_scaffold_"+stem+"_test.go"))
	}
	const hdr = "package orders\n\nimport (\n\t\"context\"\n%s\n\tpb \"example.com/connv2/gen/services/orders/v1\"\n%s)\n\n"
	unary := func(name, prefix string) string {
		return fmt.Sprintf(hdr, "", "") + fmt.Sprintf(`func (s *Service) %[1]s(ctx context.Context, req *pb.%[1]sRequest) (*pb.%[1]sResponse, error) {
	return &pb.%[1]sResponse{Text: %[2]q + req.GetText()}, nil
}
`, name, prefix)
	}
	write("rpc_ping.go", unary("Ping", "pong:"))
	write("rpc_echo.go", unary("Echo", "echo:"))
	write("rpc_public_ping.go", unary("PublicPing", "public:"))
	write("rpc_hello.go", fmt.Sprintf(hdr, "", "")+`func (s *Service) Hello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloResponse, error) {
	return &pb.HelloResponse{Greeting: "hello " + req.GetName()}, nil
}
`)
	write("rpc_fail.go", fmt.Sprintf(hdr, "\n\t\"github.com/reliant-labs/forge/pkg/svcerr\"\n", "")+`func (s *Service) Fail(ctx context.Context, req *pb.FailRequest) (*pb.FailResponse, error) {
	return nil, svcerr.Wrap(svcerr.NotFound("order"))
}
`)
	conn := "\tordersv1connect \"example.com/connv2/gen/services/orders/v1/ordersv1connect\"\n"
	write("rpc_watch.go", fmt.Sprintf(hdr, "", conn)+`func (s *Service) Watch(ctx context.Context, req *pb.WatchRequest, stream ordersv1connect.OrdersServiceWatchServerStream) error {
	for i := int32(1); i <= req.GetCount(); i++ {
		if err := stream.Send(&pb.WatchResponse{Seq: i}); err != nil {
			return err
		}
	}
	return nil
}
`)
	write("rpc_upload.go", fmt.Sprintf(hdr, "\n\t\"errors\"\n\t\"io\"\n", conn)+`func (s *Service) Upload(ctx context.Context, stream ordersv1connect.OrdersServiceUploadServerStream) (*pb.UploadResponse, error) {
	var sum int32
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return &pb.UploadResponse{Sum: sum}, nil
		}
		if err != nil {
			return nil, err
		}
		sum += msg.GetN()
	}
}
`)
	write("rpc_chat.go", fmt.Sprintf(hdr, "\n\t\"errors\"\n\t\"io\"\n", conn)+`func (s *Service) Chat(ctx context.Context, stream ordersv1connect.OrdersServiceChatServerStream) error {
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(&pb.ChatResponse{Text: "re:" + msg.GetText()}); err != nil {
			return err
		}
	}
}
`)

	// One scaffold row becomes a real v2 table: bare messages, tdd.RunRPCCasesV2.
	writeFileE2E(t, filepath.Join(ordersDir, "handlers_scaffold_ping_test.go"), `package orders_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "example.com/connv2/gen/services/orders/v1"

	orders "example.com/connv2/internal/handlers/orders"
)

func TestPing_Generated(t *testing.T) {
	t.Parallel()
	svc := orders.NewTestOrders(t)
	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.PingRequest, pb.PingResponse]{
		{
			Name: "echoes",
			Req:  &pb.PingRequest{Text: "x"},
			Ctx:  orders.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.PingResponse) {
				if resp.GetText() != "pong:x" {
					t.Fatalf("text = %q, want pong:x", resp.GetText())
				}
			},
		},
	}, svc.Ping)
}
`)
}

// connV2WireCheckTest runs inside the generated project (go test), against
// the server the e2e booted, with the project's own connect-go v2 client.
const connV2WireCheckTest = `package wirecheck

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	pb "example.com/connv2/gen/services/orders/v1"
	"example.com/connv2/gen/services/orders/v1/ordersv1connect"
)

// TestWire_V2Client skips unless the e2e that generated this project has
// booted its server (WIRE_BASE_URL) and minted a token (WIRE_TOKEN).
func TestWire_V2Client(t *testing.T) {
	base, token := os.Getenv("WIRE_BASE_URL"), os.Getenv("WIRE_TOKEN")
	if base == "" {
		t.Skip("WIRE_BASE_URL not set: this test is driven by the forge connect-v2 e2e")
	}
	// h2c: bidi streaming needs HTTP/2, and the server speaks it unencrypted.
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	httpClient := &http.Client{Transport: &http.Transport{Protocols: protocols}}
	client := ordersv1connect.NewOrdersServiceClient(connect.NewClient(connecthttp.NewTransport(httpClient, base)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	authed := func() context.Context {
		c, info := connect.NewClientContext(ctx)
		info.RequestHeader().Set("Authorization", token)
		return c
	}

	// CRUD.
	created, err := client.CreateOrder(authed(), &pb.CreateOrderRequest{Name: "v2-widget"})
	if err != nil || created.GetOrder().GetId() == "" {
		t.Fatalf("CreateOrder = %v, %v", created, err)
	}
	got, err := client.GetOrder(authed(), &pb.GetOrderRequest{Id: created.GetOrder().GetId()})
	if err != nil || got.GetOrder().GetName() != "v2-widget" {
		t.Fatalf("GetOrder = %v, %v", got, err)
	}

	// Unary, including the rpc forge scaffold rpc wrote.
	if r, err := client.Ping(authed(), &pb.PingRequest{Text: "hi"}); err != nil || r.GetText() != "pong:hi" {
		t.Fatalf("Ping = %v, %v", r, err)
	}
	if r, err := client.Hello(authed(), &pb.HelloRequest{Name: "forge"}); err != nil || r.GetGreeting() != "hello forge" {
		t.Fatalf("Hello = %v, %v", r, err)
	}

	// Errors keep their code and message.
	_, err = client.Fail(authed(), &pb.FailRequest{Id: "o-1"})
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeNotFound || !strings.Contains(cerr.Message(), "order not found") {
		t.Fatalf("Fail = %v, want not_found \"order not found\"", err)
	}

	// Auth: required by default, off where the proto says so.
	if _, err := client.Ping(ctx, &pb.PingRequest{Text: "hi"}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Ping without a token = %v, want unauthenticated", err)
	}
	if r, err := client.PublicPing(ctx, &pb.PublicPingRequest{Text: "anyone"}); err != nil || r.GetText() != "public:anyone" {
		t.Fatalf("PublicPing without a token = %v, %v", r, err)
	}

	// Server stream: frames, then a clean end.
	ws, err := client.Watch(authed(), &pb.WatchRequest{Count: 3})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	var seqs []int32
	for {
		m, err := ws.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Watch.Receive: %v", err)
		}
		seqs = append(seqs, m.GetSeq())
	}
	_ = ws.Close()
	if len(seqs) != 3 || seqs[0] != 1 || seqs[2] != 3 {
		t.Fatalf("Watch frames = %v, want [1 2 3]", seqs)
	}

	// Client stream.
	us, err := client.Upload(authed())
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	for _, n := range []int32{1, 2, 3, 4} {
		if err := us.Send(&pb.UploadRequest{N: n}); err != nil {
			t.Fatalf("Upload.Send: %v", err)
		}
	}
	if r, err := us.CloseAndReceive(); err != nil || r.GetSum() != 10 {
		t.Fatalf("Upload = %v, %v; want sum 10", r, err)
	}

	// Bidi stream.
	cs, err := client.Chat(authed())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for _, text := range []string{"a", "b"} {
		if err := cs.Send(&pb.ChatRequest{Text: text}); err != nil {
			t.Fatalf("Chat.Send: %v", err)
		}
		m, err := cs.Receive()
		if err != nil || m.GetText() != "re:"+text {
			t.Fatalf("Chat reply to %q = %v, %v", text, m, err)
		}
	}
	if err := cs.CloseSend(); err != nil {
		t.Fatalf("Chat.CloseSend: %v", err)
	}
	if _, err := cs.Receive(); !errors.Is(err, io.EOF) {
		t.Fatalf("Chat after CloseSend = %v, want io.EOF", err)
	}
	_ = cs.Close()
}
`

// unencryptedHTTP2Protocols is prior-knowledge h2c only (gRPC needs HTTP/2).
func unencryptedHTTP2Protocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return p
}

func httpGetStatus(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// postJSON POSTs a Connect unary JSON request, with Authorization when
// bearer is non-empty, and returns the status and body.
func postJSON(t *testing.T, url, bearer, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
