package mountkit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/mountkit"
)

// recordV2 returns a v2 interceptor that appends name to log on every call.
func (l *callLog) recordV2(name string) connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			l.mu.Lock()
			l.calls = append(l.calls, name)
			l.mu.Unlock()
			return next(ctx, spec, stream)
		}
	}
}

// echoV2 is a hand-built connect-go v2 service — what a v2 generated
// Register<Svc>Handler does — with one unary Echo rpc over BytesValue.
type echoV2 struct{ service string }

func (s echoV2) procedure() string { return "/" + s.service + "/Echo" }

func (s echoV2) spec() connectv2.Spec {
	return connectv2.Spec{Procedure: s.procedure(), StreamType: connectv2.StreamTypeUnary}
}

func (s echoV2) RegisterV2(server *connectv2.Server) {
	server.Register(connectv2.Method{Spec: s.spec(), Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		var req wrapperspb.BytesValue
		if err := stream.Receive(&req); err != nil {
			return err
		}
		return stream.Send(&req)
	}})
}

// echoV2HTTP adds the optional plain-HTTP capability.
type echoV2HTTP struct {
	echoV2
	route string
}

func (s echoV2HTTP) RegisterHTTP(mux *http.ServeMux, stack func(http.Handler) http.Handler) {
	mux.Handle(s.route, stack(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})))
}

func callEchoV2(t *testing.T, srv *httptest.Server, svc echoV2, payload []byte) error {
	t.Helper()
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL, connecthttp.WithReadMaxBytes(0)))
	var res wrapperspb.BytesValue
	err := client.CallUnary(context.Background(), svc.spec(), wrapperspb.Bytes(payload), &res)
	if err == nil && !bytes.Equal(res.GetValue(), payload) {
		t.Fatalf("%s echoed %d bytes, want %d", svc.procedure(), len(res.GetValue()), len(payload))
	}
	return err
}

// A v2 mount with no options of its own registers on the shared server; one
// with interceptors gets a server of its own whose chain is the shared one
// followed by its own. Each service runs exactly its chain, over real HTTP,
// and the shared interceptor slice is never written to.
func TestRegisterServiceV2_GroupsServicesByChain(t *testing.T) {
	log := &callLog{}
	shared := make([]connectv2.ServerInterceptor, 0, 8)
	shared = append(shared, log.recordV2("shared-1"), nil, log.recordV2("shared-2"))

	mux := http.NewServeMux()
	m := mountkit.NewMuxV2(mux, shared)
	plainA := echoV2{service: "test.v1.ChatService"}
	plainB := echoV2{service: "test.v1.ProjectService"}
	wake := echoV2{service: "test.v1.WorktreeService"}
	both := echoV2{service: "test.v1.FileSystemService"}

	var names []string
	names = append(names, mountkit.RegisterServiceV2(m, plainA)...)
	names = append(names, mountkit.RegisterServiceV2(m, wake, mountkit.WithInterceptorsV2(log.recordV2("wake")))...)
	names = append(names, mountkit.RegisterServiceV2(m, plainB)...)
	names = append(names, mountkit.RegisterServiceV2(m, both,
		mountkit.WithInterceptorsV2(log.recordV2("mount-1")),
		mountkit.WithInterceptorsV2(log.recordV2("mount-2")))...)

	want := []string{"test.v1.ChatService", "test.v1.WorktreeService", "test.v1.ProjectService", "test.v1.FileSystemService"}
	if !slices.Equal(names, want) {
		t.Fatalf("mounted names = %v, want %v", names, want)
	}
	// The shared server holds plainA and plainB; wake and both have their own.
	if got := len(m.Servers()); got != 3 {
		t.Fatalf("servers = %d, want 3 (one shared, one per mount with its own interceptors)", got)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, c := range []struct {
		svc  echoV2
		want string
	}{
		{plainA, "shared-1,shared-2"},
		{wake, "shared-1,shared-2,wake"},
		{plainB, "shared-1,shared-2"},
		{both, "shared-1,shared-2,mount-1,mount-2"},
	} {
		if err := callEchoV2(t, srv, c.svc, []byte("hi")); err != nil {
			t.Fatalf("call %s: %v", c.svc.procedure(), err)
		}
		if got := log.take(); got != c.want {
			t.Errorf("%s ran %q, want %q", c.svc.service, got, c.want)
		}
	}
	if shared[1] != nil {
		t.Fatal("a mount wrote into the shared interceptor slice")
	}
}

// v1ChatService is what a connect-go v1 generated New<Svc>Handler mounts: a
// "/pkg.Svc/" handler whose path switch serves the known procedures and
// sends http.NotFound for every other path under the service.
func v1ChatService(mux *http.ServeMux, service string) {
	echo := connect.NewUnaryHandler("/"+service+"/Echo",
		func(_ context.Context, req *connect.Request[wrapperspb.BytesValue]) (*connect.Response[wrapperspb.BytesValue], error) {
			return connect.NewResponse(req.Msg), nil
		})
	mux.Handle("/"+service+"/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + service + "/Echo":
			echo.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

// rawResponse is what a client reads back from an HTTP exchange, minus the
// headers that differ per exchange (Date).
type rawResponse struct {
	status      int
	contentType string
	nosniff     string
	grpcStatus  string
	body        string
}

func post(t *testing.T, url, contentType string, body []byte, header map[string]string) rawResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return rawResponse{
		status:      res.StatusCode,
		contentType: res.Header.Get("Content-Type"),
		nosniff:     res.Header.Get("X-Content-Type-Options"),
		grpcStatus:  res.Header.Get("Grpc-Status") + res.Trailer.Get("Grpc-Status"),
		body:        string(b),
	}
}

// An rpc a mounted service does not have answers a plain HTTP 404, byte for
// byte what a connect-go v1 generated handler sends, over Connect, gRPC and
// gRPC-Web — not v2's unimplemented RPC error. A client older than the
// server tells "this rpc was retired" from a failure by that 404. An unknown
// service is the mux's own 404, as before. It holds for a service on the
// shared server, one on a server of its own, and testkit's test server.
func TestRegisterServiceV2_UnknownMethodIsV1NotFound(t *testing.T) {
	const service = "test.v1.ChatService"
	v1Mux := http.NewServeMux()
	v1ChatService(v1Mux, service)
	v1 := httptest.NewServer(v1Mux)
	t.Cleanup(v1.Close)

	shared := http.NewServeMux()
	m := mountkit.NewMuxV2(shared, nil)
	svc := echoV2{service: service}
	mountkit.RegisterServiceV2(m, svc)
	// A later mount must not disturb the first service's patterns.
	mountkit.RegisterServiceV2(m, echoV2{service: "test.v1.OtherService"})
	own := http.NewServeMux()
	mountkit.RegisterServiceV2(mountkit.NewMuxV2(own, nil), svc,
		mountkit.WithInterceptorsV2(func(next connectv2.ServerFunc) connectv2.ServerFunc { return next }))
	hand := http.NewServeMux()
	handServer := connectv2.NewServer()
	svc.RegisterV2(handServer)
	mountkit.MountV2(hand, handServer)

	empty := []byte{}
	grpcFrame := []byte{0, 0, 0, 0, 0}
	requests := map[string]struct {
		contentType string
		body        []byte
		header      map[string]string
	}{
		"connect":       {"application/json", []byte("{}"), map[string]string{"Connect-Protocol-Version": "1"}},
		"connect-proto": {"application/proto", empty, map[string]string{"Connect-Protocol-Version": "1"}},
		"grpc":          {"application/grpc", grpcFrame, map[string]string{"Te": "trailers"}},
		"grpc-web":      {"application/grpc-web+proto", grpcFrame, nil},
	}
	for name, mux := range map[string]*http.ServeMux{"shared server": shared, "own server": own, "MountV2": hand} {
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		if err := callEchoV2(t, srv, svc, []byte("still here")); err != nil {
			t.Fatalf("%s: a registered rpc must still answer: %v", name, err)
		}
		for proto, r := range requests {
			for _, path := range []string{"/" + service + "/Missing", "/test.v1.NoSuchService/Echo"} {
				want := post(t, v1.URL+path, r.contentType, r.body, r.header)
				got := post(t, srv.URL+path, r.contentType, r.body, r.header)
				if want.status != http.StatusNotFound {
					t.Fatalf("precondition: v1 answers %s %s with %d", proto, path, want.status)
				}
				if got != want {
					t.Errorf("%s, %s POST %s:\n got %+v\nwant %+v (connect-go v1)", name, proto, path, got, want)
				}
			}
		}
	}

	// A v2 client reads the 404 as unimplemented, as a v1 client did.
	srv := httptest.NewServer(shared)
	t.Cleanup(srv.Close)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	err := client.CallUnary(context.Background(),
		connectv2.Spec{Procedure: "/" + service + "/Missing", StreamType: connectv2.StreamTypeUnary},
		&emptypb.Empty{}, &emptypb.Empty{})
	if got := connectv2.CodeOf(err); got != connectv2.CodeUnimplemented {
		t.Fatalf("unknown method: a v2 client reads %v (%v), want unimplemented", got, err)
	}
}

// A v2 mount reads a request of any size by default, as a v1 handler does
// (v2's own default is 4 MiB); a read limit passed to NewMuxV2 applies, and a
// mount's WithHTTPOptionsV2 overrides it for that mount alone.
func TestRegisterServiceV2_ReadLimitParity(t *testing.T) {
	big := bytes.Repeat([]byte{'x'}, 5<<20)

	mux := http.NewServeMux()
	unbounded := echoV2{service: "test.v1.Unbounded"}
	mountkit.RegisterServiceV2(mountkit.NewMuxV2(mux, nil), unbounded)

	capped := mountkit.NewMuxV2(mux, nil, connecthttp.WithReadMaxBytes(1<<10))
	small := echoV2{service: "test.v1.Small"}
	lifted := echoV2{service: "test.v1.Lifted"}
	mountkit.RegisterServiceV2(capped, small)
	mountkit.RegisterServiceV2(capped, lifted, mountkit.WithHTTPOptionsV2(connecthttp.WithReadMaxBytes(0)))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := callEchoV2(t, srv, unbounded, big); err != nil {
		t.Fatalf("5 MiB through a mount with no read limit: %v", err)
	}
	if err := callEchoV2(t, srv, small, big); connectv2.CodeOf(err) != connectv2.CodeResourceExhausted {
		t.Fatalf("5 MiB through a 1 KiB cap: %v, want resource_exhausted", err)
	}
	if err := callEchoV2(t, srv, lifted, big); err != nil {
		t.Fatalf("5 MiB through a mount that lifted the cap: %v", err)
	}
}

// RegisterServiceV2 invokes the optional HTTPRegistrar with the
// WithHTTPStack stack, on the same mux.
func TestRegisterServiceV2_OptionalHTTPRoutes(t *testing.T) {
	mux := http.NewServeMux()
	m := mountkit.NewMuxV2(mux, nil)
	stacked := false
	svc := echoV2HTTP{echoV2: echoV2{service: "test.v1.HookService"}, route: "/hooks/in"}
	mountkit.RegisterServiceV2(m, svc, mountkit.WithHTTPStack(func(next http.Handler) http.Handler {
		stacked = true
		return next
	}))
	if !stacked {
		t.Fatal("RegisterHTTP was not handed the WithHTTPStack stack")
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resp, err := srv.Client().Post(srv.URL+"/hooks/in", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("plain HTTP route: status %d, want 202", resp.StatusCode)
	}
	if err := callEchoV2(t, srv, svc.echoV2, []byte("x")); err != nil {
		t.Fatalf("connect route: %v", err)
	}
}

// A mount option of the other major is a boot error, never silently
// dropped; so is a value with no RegisterV2.
func TestMountOptionsRefuseTheOtherMajor(t *testing.T) {
	mustPanic := func(name, want string, f func()) {
		t.Helper()
		defer func() {
			r := recover()
			msg, _ := r.(string)
			if r == nil || !strings.Contains(msg, want) {
				t.Errorf("%s: recovered %v, want a panic mentioning %q", name, r, want)
			}
		}()
		f()
	}
	v2Opt := mountkit.WithInterceptorsV2(func(next connectv2.ServerFunc) connectv2.ServerFunc { return next })
	mustPanic("v1 Resolve with a v2 interceptor", "WithInterceptorsV2", func() {
		mountkit.Resolve(nil, nil, v2Opt)
	})
	mustPanic("v1 RegisterService with v2 HTTP options", "WithHTTPOptionsV2", func() {
		mountkit.RegisterService(http.NewServeMux(), pingService{procedure: "/t.v1.S/P"}, nil,
			mountkit.WithHTTPOptionsV2(connecthttp.WithReadMaxBytes(1)))
	})
	mustPanic("v2 mount with v1 handler options", "WithHandlerOptions", func() {
		mountkit.RegisterServiceV2(mountkit.NewMuxV2(http.NewServeMux(), nil), echoV2{service: "t.v1.S"},
			mountkit.WithHandlerOptions(connect.WithReadMaxBytes(1)))
	})
	mustPanic("v1-only service on a v2 mount", "RegistrarV2", func() {
		mountkit.RegisterServiceV2(mountkit.NewMuxV2(http.NewServeMux(), nil), pingService{procedure: "/t.v1.S/P"})
	})
	mustPanic("nil mux", "nil mux", func() { mountkit.NewMuxV2(nil, nil) })
}

// A service type carrying both registrations mounts on either major: the v1
// path calls Register, the v2 path RegisterV2.
func TestDualMajorServiceMountsOnEither(t *testing.T) {
	svc := dualService{echoV2: echoV2{service: "test.v1.DualService"}}
	v1mux, v2mux := http.NewServeMux(), http.NewServeMux()
	mountkit.RegisterService(v1mux, svc, nil)
	mountkit.RegisterServiceV2(mountkit.NewMuxV2(v2mux, nil), svc)

	for name, mux := range map[string]*http.ServeMux{"v1": v1mux, "v2": v2mux} {
		srv := httptest.NewServer(mux)
		err := callEchoV2(t, srv, svc.echoV2, []byte(name))
		srv.Close()
		if err != nil {
			t.Errorf("%s mount: %v", name, err)
		}
	}
}

type dualService struct{ echoV2 }

func (s dualService) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	mux.Handle(s.procedure(), connect.NewUnaryHandler(s.procedure(),
		func(_ context.Context, req *connect.Request[wrapperspb.BytesValue]) (*connect.Response[wrapperspb.BytesValue], error) {
			if req.Msg == nil {
				return nil, errors.New("no message")
			}
			return connect.NewResponse(req.Msg), nil
		}, opts...))
}
