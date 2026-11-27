package serverkit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The connect-go v2 fixtures: two more user-audience services (so a route set
// can mount several on one listener), a token authenticator, and a Ping
// service registered the way a v2 generated Register<Svc>Handler does.
const (
	fixtureAlphaService = fixturePkg + "v2.AlphaService"
	fixtureBetaService  = fixturePkg + "v2.BetaService"
)

var registerV2File sync.Once

func registerV2Services(t *testing.T) {
	t.Helper()
	registerAudienceServices(t)
	registerV2File.Do(func() {
		svc := func(name string) *descriptorpb.ServiceDescriptorProto {
			return &descriptorpb.ServiceDescriptorProto{
				Name: proto.String(name),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       proto.String("Ping"),
					InputType:  proto.String(".google.protobuf.Empty"),
					OutputType: proto.String(".google.protobuf.Empty"),
				}},
			}
		}
		fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
			Name:       proto.String("serverkit_composition_v2.proto"),
			Package:    proto.String(fixturePkg + "v2"),
			Syntax:     proto.String("proto3"),
			Dependency: []string{"google/protobuf/empty.proto"},
			Service:    []*descriptorpb.ServiceDescriptorProto{svc("AlphaService"), svc("BetaService")},
		}, protoregistry.GlobalFiles)
		if err != nil {
			t.Fatalf("protodesc.NewFile: %v", err)
		}
		if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
			t.Fatalf("RegisterFile: %v", err)
		}
	})
}

// tokenAuthV2 admits a request whose Authorization header is "Bearer <token>",
// rejecting any other with tokenAuth's exact error.
func tokenAuthV2(token string) connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			info, _ := connectv2.CallInfoForServerContext(ctx)
			if info.RequestHeader().Get("Authorization") != "Bearer "+token {
				return connectv2.NewError(connectv2.CodeUnauthenticated, "wrong credential")
			}
			return next(ctx, spec, stream)
		}
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// chainForV2 is chainFor for connect-go v2.
func chainForV2(t *testing.T, audience, token string) *serverkit.ChainV2 {
	t.Helper()
	c, err := serverkit.NewChainV2(serverkit.ChainDepsV2{
		Auth:   serverkit.AuthWithCallerV2(audience, tokenAuthV2(token), authn.ClaimsFromContext),
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewChainV2(%s): %v", audience, err)
	}
	return c
}

// behave is the shared behaviour of the parity fixtures: the X-Case request
// header picks what the handler returns.
func behave(c string) error {
	switch c {
	case "notfound":
		return svcerr.NotFound("widget")
	case "invalid":
		return svcerr.InvalidArgument("name is required")
	case "plain":
		return errors.New("db exploded: password=hunter2")
	case "reason":
		return svcerr.WithMetadata(svcerr.WithReason(svcerr.PlanLimit("upgrade to add seats"), "PLAN_LIMIT"), "x-plan", "free")
	case "detail":
		return svcerr.WithDetail(svcerr.InvalidArgument("bad"), wrapperspb.String("field: name"))
	}
	return nil
}

// pingV2 is a v2 service with one Ping rpc, its behaviour picked by X-Case.
type pingV2 struct{ service string }

func (p pingV2) RegisterV2(server *connectv2.Server) {
	var schema any
	if d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(p.service + ".Ping")); err == nil {
		schema = d
	}
	server.Register(connectv2.Method{
		Spec: connectv2.Spec{Procedure: pingProcedure(p.service), StreamType: connectv2.StreamTypeUnary, Schema: schema},
		Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req emptypb.Empty
			if err := stream.Receive(&req); err != nil {
				return err
			}
			info, _ := connectv2.CallInfoForServerContext(ctx)
			if err := behave(info.RequestHeader().Get("X-Case")); err != nil {
				return err
			}
			return stream.Send(&emptypb.Empty{})
		},
	})
}

// pingRoutesV2 is pingRoutes for v2: each service mounted through the
// listener's MuxV2, the per-service mount options from opts.
func pingRoutesV2(opts map[string][]mountkit.Option, services ...string) serverkit.RoutesFuncV2 {
	return func(m *mountkit.MuxV2) []string {
		var names []string
		for _, s := range services {
			names = append(names, mountkit.RegisterServiceV2(m, pingV2{service: s}, opts[s]...)...)
		}
		return names
	}
}

// pingV1 is the v1 twin of pingV2, for the parity test.
func pingRoutesV1Behaving(services ...string) serverkit.RoutesFunc {
	return func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
		for _, s := range services {
			p := pingProcedure(s)
			mux.Handle(p, connect.NewUnaryHandler(p,
				func(_ context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					if err := behave(req.Header().Get("X-Case")); err != nil {
						return nil, err
					}
					return connect.NewResponse(&emptypb.Empty{}), nil
				}, opts...))
		}
		return services
	}
}

// pingV2Client calls service's Ping on base over a v2 client with token.
func pingV2Client(base, service, token string) (*connectv2.CallInfo, error) {
	client := connectv2.NewClient(connecthttp.NewTransport(http.DefaultClient, base))
	ctx, info := connectv2.NewClientContext(context.Background())
	info.RequestHeader().Set("Authorization", "Bearer "+token)
	err := client.CallUnary(ctx, connectv2.Spec{Procedure: pingProcedure(service), StreamType: connectv2.StreamTypeUnary},
		&emptypb.Empty{}, &emptypb.Empty{})
	return info, err
}

// A v2 listener serves its routes through its v2 chain: the credential is
// checked (v1 and v2 clients alike — the wire is the same), each mount runs
// exactly its own chain (the shared one, plus its WithInterceptorsV2 on a
// server of its own), the health rpc answers through the chain, and the
// services it mounted are named for the audience check.
func TestListenerV2_ServesThroughTheChain(t *testing.T) {
	registerV2Services(t)
	marker := func(v string) connectv2.ServerInterceptor {
		return func(next connectv2.ServerFunc) connectv2.ServerFunc {
			return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
				info, _ := connectv2.CallInfoForServerContext(ctx)
				info.ResponseHeader().Add("X-Mount", v)
				return next(ctx, spec, stream)
			}
		}
	}
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config:  loopback(),
		ChainV2: chainForV2(t, serverkit.AudienceUser, "user-token"),
		RoutesV2: pingRoutesV2(map[string][]mountkit.Option{
			fixtureBetaService: {mountkit.WithInterceptorsV2(marker("beta"))},
		}, fixtureUserService, fixtureAlphaService, fixtureBetaService),
		Front: map[string]http.Handler{serverkit.DefaultReadinessPath: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})},
	})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	if got, want := l.Services(), []string{fixtureUserService, fixtureAlphaService, fixtureBetaService}; !slices.Equal(got, want) {
		t.Fatalf("Services = %v, want %v", got, want)
	}
	if l.Audience() != serverkit.AudienceUser {
		t.Fatalf("Audience = %q", l.Audience())
	}
	base, _ := startListener(t, l)

	if err := ping(base, fixtureUserService, "user-token"); err != nil {
		t.Fatalf("v1 client, v2 listener: %v", err)
	}
	if err := ping(base, fixtureUserService, "wrong"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a wrong credential: %v, want Unauthenticated", err)
	}
	for _, c := range []struct{ service, mark string }{
		{fixtureAlphaService, ""},
		{fixtureBetaService, "beta"},
		{fixtureUserService, ""},
	} {
		info, err := pingV2Client(base, c.service, "user-token")
		if err != nil {
			t.Fatalf("v2 client, %s: %v", c.service, err)
		}
		if got := info.ResponseHeader().Get("X-Mount"); got != c.mark {
			t.Errorf("%s ran mount interceptor %q, want %q", c.service, got, c.mark)
		}
	}
	if _, err := pingV2Client(base, fixtureBetaService, "wrong"); connectv2.CodeOf(err) != connectv2.CodeUnauthenticated {
		t.Fatalf("the shared chain wraps a mount's own server: %v, want Unauthenticated", err)
	}

	health := connect.NewClient[healthpb.HealthCheckRequest, healthpb.HealthCheckResponse](http.DefaultClient, base+serverkit.HealthCheckProcedure)
	req := connect.NewRequest(&healthpb.HealthCheckRequest{})
	req.Header().Set("Authorization", "Bearer user-token")
	resp, err := health.CallUnary(context.Background(), req)
	if err != nil || resp.Msg.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health rpc: %v %v, want SERVING", resp, err)
	}
	req = connect.NewRequest(&healthpb.HealthCheckRequest{Service: "other"})
	req.Header().Set("Authorization", "Bearer user-token")
	if _, err := health.CallUnary(context.Background(), req); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("health rpc for a named service: %v, want NotFound", err)
	}
	req = connect.NewRequest(&healthpb.HealthCheckRequest{})
	if _, err := health.CallUnary(context.Background(), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("health rpc runs through the chain: %v, want Unauthenticated", err)
	}
}

// The v2 listener's audience check is the v1 one, and its required deps say
// what is missing.
func TestNewListenerV2_AudienceAndRequiredDeps(t *testing.T) {
	registerV2Services(t)
	daemon := chainForV2(t, audienceDaemon, "t")
	if _, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: loopback(), ChainV2: daemon, RoutesV2: pingRoutesV2(nil, fixtureDaemonService, fixtureUserService),
	}); err == nil || !strings.Contains(err.Error(), fixtureUserService+` (audience "user")`) {
		t.Fatalf("daemon chain, user service: %v", err)
	}
	if _, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: loopback(), ChainV2: daemon, RoutesV2: pingRoutesV2(nil, fixtureDaemonService),
	}); err != nil {
		t.Fatalf("daemon chain, daemon service: %v", err)
	}
	for name, tc := range map[string]struct {
		deps serverkit.ListenerDeps
		want string
	}{
		"routes without a chain": {serverkit.ListenerDeps{Config: loopback(), RoutesV2: pingRoutesV2(nil)}, "ChainV2 is required"},
		"chain without routes":   {serverkit.ListenerDeps{Config: loopback(), ChainV2: daemon}, "Routes is required"},
		"two audiences": {serverkit.ListenerDeps{
			Config: loopback(), ChainV2: daemon, RoutesV2: pingRoutesV2(nil),
			Chain: chainFor(t, serverkit.AudienceUser, "t"), Routes: pingRoutes(),
		}, "one listener serves one audience"},
		"v1 routes without a v1 chain": {serverkit.ListenerDeps{
			Config: loopback(), ChainV2: daemon, RoutesV2: pingRoutesV2(nil), Routes: pingRoutes(),
		}, "Chain is required with Routes"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serverkit.NewListener(tc.deps); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewListener err = %v, want %q", err, tc.want)
			}
		})
	}
}

// One listener may serve v1 routes through a v1 chain and v2 routes through a
// v2 chain on one mux while a project migrates.
func TestListener_ServesBothMajorsOnOneMux(t *testing.T) {
	registerV2Services(t)
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config:   loopback(),
		Chain:    chainFor(t, serverkit.AudienceUser, "tok"),
		Routes:   pingRoutes(fixtureUserService),
		ChainV2:  chainForV2(t, serverkit.AudienceUser, "tok"),
		RoutesV2: pingRoutesV2(nil, fixtureAlphaService),
	})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := startListener(t, l)
	for _, s := range []string{fixtureUserService, fixtureAlphaService} {
		if err := ping(base, s, "tok"); err != nil {
			t.Errorf("%s: %v", s, err)
		}
		if err := ping(base, s, "nope"); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s without its credential: %v", s, err)
		}
	}
}

// wireResponse is what a client sees of one unary call.
type wireResponse struct {
	status int
	header http.Header
	body   []byte
}

// volatileHeaders differ between any two calls, whatever served them.
var volatileHeaders = []string{"Date", "X-Request-Id", "Traceresponse"}

func callRaw(t *testing.T, base, service, contentType string, body []byte, header http.Header) wireResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+pingProcedure(service), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Header.Clone()
	for _, k := range volatileHeaders {
		h.Del(k)
	}
	return wireResponse{status: resp.StatusCode, header: h, body: b}
}

// The same service behaviour served by the v1 serverkit (NewChain + Routes)
// and the v2 serverkit (NewChainV2 + RoutesV2) is the same on the wire:
// status, every non-volatile header and the body, byte for byte, for a
// success, svcerr errors (with a reason, metadata and a detail), a plain
// error (redacted to internal by svcerr in both), and an auth denial — over
// Connect JSON and Connect binary — and the same code, message, details and
// metadata over gRPC.
func TestServerkitV2_WireParityWithV1(t *testing.T) {
	registerV2Services(t)
	v1Chain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, tokenAuth{token: "tok"}), Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), Chain: v1Chain, Routes: pingRoutesV1Behaving(fixtureUserService)})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), ChainV2: chainForV2(t, serverkit.AudienceUser, "tok"), RoutesV2: pingRoutesV2(nil, fixtureUserService)})
	if err != nil {
		t.Fatal(err)
	}
	v1Base, _ := startListener(t, v1)
	v2Base, _ := startListener(t, v2)

	emptyProto, _ := proto.Marshal(&emptypb.Empty{})
	cases := []struct {
		name  string
		token string
		kase  string
	}{
		{"success", "tok", ""},
		{"svcerr not found", "tok", "notfound"},
		{"svcerr invalid argument", "tok", "invalid"},
		{"plain error", "tok", "plain"},
		{"reason and metadata", "tok", "reason"},
		{"error detail", "tok", "detail"},
		{"auth denial", "wrong", ""},
		{"no credential", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{"X-Case": {tc.kase}}
			if tc.token != "" {
				header.Set("Authorization", "Bearer "+tc.token)
			}
			for _, enc := range []struct {
				contentType string
				body        []byte
			}{
				{"application/json", []byte("{}")},
				{"application/proto", emptyProto},
			} {
				want := callRaw(t, v1Base, fixtureUserService, enc.contentType, enc.body, header)
				got := callRaw(t, v2Base, fixtureUserService, enc.contentType, enc.body, header)
				// The comparison is over responses that say something.
				switch tc.kase {
				case "reason":
					if want.header.Get("X-Plan") != "free" {
						t.Fatalf("v1 response carries no error metadata to compare: %v", want.header)
					}
				case "plain":
					if bytes.Contains(got.body, []byte("hunter2")) || !bytes.Contains(got.body, []byte(svcerr.InternalMessage)) {
						t.Fatalf("v2 leaked or lost the redaction: %s", got.body)
					}
				}
				if got.status != want.status {
					t.Errorf("%s: status v2 %d, v1 %d", enc.contentType, got.status, want.status)
				}
				if !bytes.Equal(got.body, want.body) {
					t.Errorf("%s: body\n v2 %s\n v1 %s", enc.contentType, got.body, want.body)
				}
				for _, k := range unionKeys(got.header, want.header) {
					if !slices.Equal(got.header.Values(k), want.header.Values(k)) {
						t.Errorf("%s: header %s: v2 %q, v1 %q", enc.contentType, k, got.header.Values(k), want.header.Values(k))
					}
				}
			}

			// gRPC, through a v1 client over h2c, against both.
			call := func(base string) (*connect.Response[emptypb.Empty], error) {
				client := connect.NewClient[emptypb.Empty, emptypb.Empty](h2cClient(), base+pingProcedure(fixtureUserService), connect.WithGRPC())
				req := connect.NewRequest(&emptypb.Empty{})
				for k, v := range header {
					req.Header()[k] = v
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return client.CallUnary(ctx, req)
			}
			_, wantErr := call(v1Base)
			_, gotErr := call(v2Base)
			if connect.CodeOf(gotErr) != connect.CodeOf(wantErr) {
				t.Fatalf("grpc: code v2 %v, v1 %v (%v / %v)", connect.CodeOf(gotErr), connect.CodeOf(wantErr), gotErr, wantErr)
			}
			var g, w *connect.Error
			if errors.As(gotErr, &g) != errors.As(wantErr, &w) {
				t.Fatalf("grpc: error v2 %v, v1 %v", gotErr, wantErr)
			}
			if g == nil {
				return
			}
			if g.Message() != w.Message() {
				t.Errorf("grpc: message v2 %q, v1 %q", g.Message(), w.Message())
			}
			if len(g.Details()) != len(w.Details()) {
				t.Errorf("grpc: %d details on v2, %d on v1", len(g.Details()), len(w.Details()))
			}
			for i := range min(len(g.Details()), len(w.Details())) {
				if g.Details()[i].Type() != w.Details()[i].Type() || !bytes.Equal(g.Details()[i].Bytes(), w.Details()[i].Bytes()) {
					t.Errorf("grpc: detail %d differs: v2 %s, v1 %s", i, g.Details()[i].Type(), w.Details()[i].Type())
				}
			}
			for _, k := range unionKeys(g.Meta(), w.Meta()) {
				if k == "Date" || k == "X-Request-Id" || k == "Traceresponse" || strings.HasPrefix(k, "Grpc-") || k == "Content-Type" {
					continue
				}
				if !slices.Equal(g.Meta().Values(k), w.Meta().Values(k)) {
					t.Errorf("grpc: metadata %s: v2 %q, v1 %q", k, g.Meta().Values(k), w.Meta().Values(k))
				}
			}
		})
	}
}

func unionKeys(a, b http.Header) []string {
	var keys []string
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// serverkit.Run over a Server configured for v2: Mount takes a RegistrarV2
// through ChainV2, the health rpc is mounted on the same chain, and the
// recorded services are checked against ChainV2's audience.
func TestRunV2_MountServesThroughChainV2(t *testing.T) {
	requireSelfSIGTERM(t)
	registerV2Services(t)
	srv := serverkit.Server{Mux: http.NewServeMux(), ChainV2: chainForV2(t, serverkit.AudienceUser, "tok")}
	srv.Mount(fixtureAlphaService, pingV2{service: fixtureAlphaService})
	srv.Mount(fixtureBetaService, pingV2{service: fixtureBetaService},
		mountkit.WithInterceptorsV2(func(next connectv2.ServerFunc) connectv2.ServerFunc {
			return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
				return svcerr.PermissionDenied("beta is closed")
			}
		}))
	if got := srv.MountedNames(); !slices.Equal(got, []string{fixtureAlphaService, fixtureBetaService}) {
		t.Fatalf("MountedNames = %v", got)
	}
	addr := freeAddr(t)
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 5*time.Second)
	base := "http://" + addr

	if err := ping(base, fixtureAlphaService, "tok"); err != nil {
		t.Fatalf("alpha: %v", err)
	}
	if err := ping(base, fixtureAlphaService, "bad"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("alpha without its credential: %v", err)
	}
	if err := ping(base, fixtureBetaService, "tok"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("beta's own interceptor (inside svcerr's reach): %v, want PermissionDenied", err)
	}
	health := connect.NewClient[healthpb.HealthCheckRequest, healthpb.HealthCheckResponse](http.DefaultClient, base+serverkit.HealthCheckProcedure)
	req := connect.NewRequest(&healthpb.HealthCheckRequest{})
	req.Header().Set("Authorization", "Bearer tok")
	if resp, err := health.CallUnary(context.Background(), req); err != nil || resp.Msg.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health rpc: %v %v", resp, err)
	}
	if err := shutdownAndWait(t, errCh, 10*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A service of one major on a Server configured only for the other is a boot
// panic, never a mount with no chain; a Server whose ChainV2 admits another
// audience than its recorded services declare fails Run.
func TestServerMountV2_RefusesTheOtherMajorAndChecksAudience(t *testing.T) {
	registerV2Services(t)
	mustPanic := func(name, want string, f func()) {
		t.Helper()
		defer func() {
			r := recover()
			if msg, _ := r.(string); r == nil || !strings.Contains(msg, want) {
				t.Errorf("%s: recovered %v, want a panic naming %q", name, r, want)
			}
		}()
		f()
	}
	mustPanic("v2 service, v1 server", "configured only for v1", func() {
		srv := serverkit.Server{Mux: http.NewServeMux(), Chain: chainFor(t, serverkit.AudienceUser, "t")}
		srv.Mount(fixtureAlphaService, pingV2{service: fixtureAlphaService})
	})
	mustPanic("v1 service, v2 server", "configured only for v2", func() {
		srv := serverkit.Server{Mux: http.NewServeMux(), ChainV2: chainForV2(t, serverkit.AudienceUser, "t")}
		srv.Mount(fixtureUserService, v1Only{})
	})

	srv := serverkit.Server{Mux: http.NewServeMux(), ChainV2: chainForV2(t, audienceDaemon, "t")}
	srv.Mount(fixtureUserService, pingV2{service: fixtureUserService})
	err := serverkit.Run(context.Background(), baseConfig(freeAddr(t)), srv)
	if err == nil || !strings.Contains(err.Error(), fixtureUserService) {
		t.Fatalf("Run = %v, want the audience check naming %s", err, fixtureUserService)
	}
}

type v1Only struct{}

func (v1Only) Register(*http.ServeMux, ...connect.HandlerOption) {}

// NewChainV2 refuses what NewChain refuses, and a slot both set and dropped.
func TestNewChainV2_Refusals(t *testing.T) {
	ic := tokenAuthV2("t")
	noCaller := serverkit.AuthForV2(serverkit.AudienceUser, ic)
	for name, tc := range map[string]struct {
		deps serverkit.ChainDepsV2
		want string
	}{
		"no auth":          {serverkit.ChainDepsV2{}, "ChainDepsV2.Auth is required"},
		"no audience":      {serverkit.ChainDepsV2{Auth: serverkit.AuthForV2("", ic)}, "names no audience"},
		"nil interceptor":  {serverkit.ChainDepsV2{Auth: serverkit.AuthForV2(serverkit.AudienceUser, nil), NoAudit: true, NoActivity: true}, "nil interceptor"},
		"nil named caller": {serverkit.ChainDepsV2{Auth: serverkit.AuthWithCallerV2(serverkit.AudienceUser, ic, nil)}, "was given a nil caller"},
		"default audit":    {serverkit.ChainDepsV2{Auth: noCaller, NoActivity: true}, "default Audit would audit every call as anonymous"},
		"default activity": {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true}, "default Activity would record every event"},
		"set and dropped":  {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true, Validate: ic, NoValidate: true}, "Validate is set and NoValidate drops it"},
		"deadline dropped": {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true, Deadline: serverkit.Deadline{Unary: time.Second}, NoDeadline: true}, "NoDeadline drops the slot"},
		"deadline twice":   {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true, Deadline: serverkit.Deadline{Unary: time.Second}, DeadlineOverride: ic}, "both set"},
		"bad deadline":     {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true, Deadline: serverkit.Deadline{Procedures: map[string]time.Duration{"nope": time.Second}}}, "is not a procedure"},
		"nil extra":        {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true, Extras: []connectv2.ServerInterceptor{nil}}, "Extras[0] is nil"},
		"nil outer":        {serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true, Outer: []connectv2.ServerInterceptor{nil}}, "Outer[0] is nil"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serverkit.NewChainV2(tc.deps); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewChainV2 err = %v, want %q", err, tc.want)
			}
		})
	}
	// Dropping both caller-recording slots builds without a caller.
	if _, err := serverkit.NewChainV2(serverkit.ChainDepsV2{Auth: noCaller, NoAudit: true, NoActivity: true}); err != nil {
		t.Fatalf("NoAudit + NoActivity over a caller-less auth: %v", err)
	}
}

// AuthPolicyV2 reads the caller from the authn.Policy, as AuthFor reads it off
// authn's v1 interceptor, so the default Audit and Activity build.
func TestAuthPolicyV2_ReadsTheCallerFromThePolicy(t *testing.T) {
	a, err := serverkit.AuthPolicyV2(serverkit.AudienceUser, authn.Policy{
		ValidatorConfigured: true,
		ValidateContext:     func(context.Context, string) (*authn.Claims, error) { return &authn.Claims{UserID: "u"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Caller() == nil || a.ServerInterceptor() == nil || a.Audience() != serverkit.AudienceUser {
		t.Fatalf("AuthPolicyV2 = caller %v, interceptor %v, audience %q", a.Caller() != nil, a.ServerInterceptor() != nil, a.Audience())
	}
	if _, err := serverkit.NewChainV2(serverkit.ChainDepsV2{Auth: a, Logger: discardLogger()}); err != nil {
		t.Fatalf("NewChainV2 over AuthPolicyV2: %v", err)
	}
	if _, err := serverkit.AuthPolicyV2(serverkit.AudienceUser, authn.Policy{}); err == nil {
		t.Fatal("AuthPolicyV2 accepted a policy authn refuses")
	}
}

// NewChainV2's payload caps match NewChain's: zero is 4 MiB, negative is no
// cap — on the wire, through a listener.
func TestChainV2_PayloadCaps(t *testing.T) {
	registerV2Services(t)
	build := func(readMax int) string {
		c, err := serverkit.NewChainV2(serverkit.ChainDepsV2{
			Auth:         serverkit.AuthWithCallerV2(serverkit.AudienceUser, tokenAuthV2("tok"), authn.ClaimsFromContext),
			Logger:       discardLogger(),
			ReadMaxBytes: readMax,
			NoValidate:   true,
		})
		if err != nil {
			t.Fatal(err)
		}
		l, err := serverkit.NewListener(serverkit.ListenerDeps{Config: loopback(), ChainV2: c, RoutesV2: serverkit.RoutesFuncV2(func(m *mountkit.MuxV2) []string {
			return m.Register(func(s *connectv2.Server) {
				s.Register(connectv2.Method{
					Spec: connectv2.Spec{Procedure: pingProcedure(fixtureAlphaService), StreamType: connectv2.StreamTypeUnary},
					Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
						var b wrapperspb.BytesValue
						if err := stream.Receive(&b); err != nil {
							return err
						}
						return stream.Send(&emptypb.Empty{})
					},
				})
			})
		})})
		if err != nil {
			t.Fatal(err)
		}
		base, _ := startListener(t, l)
		return base
	}
	send := func(base string, n int) error {
		client := connectv2.NewClient(connecthttp.NewTransport(http.DefaultClient, base, connecthttp.WithReadMaxBytes(0)))
		ctx, info := connectv2.NewClientContext(context.Background())
		info.RequestHeader().Set("Authorization", "Bearer tok")
		return client.CallUnary(ctx, connectv2.Spec{Procedure: pingProcedure(fixtureAlphaService), StreamType: connectv2.StreamTypeUnary},
			wrapperspb.Bytes(make([]byte, n)), &emptypb.Empty{})
	}
	def, unbounded := build(0), build(-1)
	if err := send(def, 5<<20); connectv2.CodeOf(err) != connectv2.CodeResourceExhausted {
		t.Fatalf("5 MiB through the default 4 MiB cap: %v, want resource_exhausted", err)
	}
	if err := send(def, 1<<20); err != nil {
		t.Fatalf("1 MiB through the default cap: %v", err)
	}
	if err := send(unbounded, 5<<20); err != nil {
		t.Fatalf("5 MiB with no cap: %v", err)
	}
}
