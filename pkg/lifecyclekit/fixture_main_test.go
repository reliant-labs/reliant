package lifecyclekit_test

// fixture_main_test.go — a hand-written main with TWO listeners under
// Supervise: the shape a process takes when it serves two kinds of caller
// (reliant's API and its daemon gateway; composition-graph design §6, step 1).
// Each listener has its own chain, authenticating its own audience, and its
// own address; mounting a user service on the daemon listener is a boot
// error, before anything serves.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/forgepb"
	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

const (
	fixturePkg     = "lifecyclekit.fixture.v1"
	userService    = fixturePkg + ".UserService"
	daemonService  = fixturePkg + ".ToolsDaemonService"
	audienceDaemon = "daemon"
)

var registerFixture sync.Once

// registerFixtureServices puts the two services in the global proto registry:
// UserService declares no audience (user), ToolsDaemonService declares
// `option (forge.v1.service) = {audience: "daemon"}`.
func registerFixtureServices(t *testing.T) {
	t.Helper()
	registerFixture.Do(func() {
		daemonOpts := &descriptorpb.ServiceOptions{}
		proto.SetExtension(daemonOpts, forgepb.E_Service, &forgepb.ServiceOptions{Audience: audienceDaemon})
		svc := func(name string, opts *descriptorpb.ServiceOptions) *descriptorpb.ServiceDescriptorProto {
			return &descriptorpb.ServiceDescriptorProto{
				Name: proto.String(name),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       proto.String("Ping"),
					InputType:  proto.String(".google.protobuf.Empty"),
					OutputType: proto.String(".google.protobuf.Empty"),
				}},
				Options: opts,
			}
		}
		fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
			Name:       proto.String("lifecyclekit_fixture.proto"),
			Package:    proto.String(fixturePkg),
			Syntax:     proto.String("proto3"),
			Dependency: []string{"google/protobuf/empty.proto", "forge/v1/forge.proto"},
			Service:    []*descriptorpb.ServiceDescriptorProto{svc("UserService", nil), svc("ToolsDaemonService", daemonOpts)},
		}, protoregistry.GlobalFiles)
		if err != nil {
			t.Fatalf("protodesc.NewFile: %v", err)
		}
		if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
			t.Fatalf("RegisterFile: %v", err)
		}
	})
}

// bearer admits "Authorization: Bearer <token>".
type bearer string

// Claims names forge authn's claims reader, as authn's own interceptor does, so
// AuthFor gives this fixture a Caller and the chain's default Audit and
// Activity slots build.
func (bearer) Claims() func(context.Context) (*authn.Claims, bool) { return authn.ClaimsFromContext }

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Header().Get("Authorization") != "Bearer "+string(b) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("wrong credential"))
		}
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// pingRoutes mounts Ping for each service with the listener's chain.
func pingRoutes(services ...string) serverkit.RoutesFunc {
	return func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
		for _, s := range services {
			p := "/" + s + "/Ping"
			mux.Handle(p, connect.NewUnaryHandler(p,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					return connect.NewResponse(&emptypb.Empty{}), nil
				}, opts...))
		}
		return services
	}
}

// fixtureListeners is what the fixture main builds before it supervises.
type fixtureListeners struct {
	drainer     *serverkit.Drainer
	api, daemon *serverkit.Listener
	apiNode     lifecyclekit.Node
	daemonNode  lifecyclekit.Node
}

// buildFixture is the composition half of the fixture main: two chains, two
// listeners. daemonRoutes is what the daemon listener mounts — the fixture's
// one knob, so a test can mount a user service where it does not belong.
func buildFixture(logger *slog.Logger, daemonRoutes ...string) (*fixtureListeners, error) {
	drainer := &serverkit.Drainer{PreStopDelay: -1, Logger: logger}
	userChain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(serverkit.AudienceUser, bearer("user")), Logger: logger})
	if err != nil {
		return nil, err
	}
	daemonChain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(audienceDaemon, bearer("daemon")), Logger: logger})
	if err != nil {
		return nil, err
	}
	api, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: serverkit.ListenerConfig{Addr: "127.0.0.1:0"},
		Chain:  userChain,
		Routes: pingRoutes(userService),
		Logger: logger,
		Front:  map[string]http.Handler{"/readyz": drainer.ReadinessHandler(nil, 0, logger)},
	})
	if err != nil {
		return nil, err
	}
	daemon, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: serverkit.ListenerConfig{Addr: "127.0.0.1:0", ReadTimeout: -1, RedialOnDrain: []string{daemonService}},
		Chain:  daemonChain,
		Routes: pingRoutes(daemonRoutes...),
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return &fixtureListeners{
		drainer: drainer, api: api, daemon: daemon,
		apiNode: lifecyclekit.Component("api", api), daemonNode: lifecyclekit.Component("daemon", daemon),
	}, nil
}

// fixtureMain is the hand-written main: compose, then Supervise until ctx —
// the signal context — ends. The daemon listener is listed last, so it is
// the first to drain.
func fixtureMain(ctx context.Context, logger *slog.Logger, booted chan<- *fixtureListeners, daemonRoutes ...string) error {
	f, err := buildFixture(logger, daemonRoutes...)
	if err != nil {
		return err
	}
	booted <- f
	return lifecyclekit.Supervise(ctx, lifecyclekit.Policy{Drainer: f.drainer}, f.apiNode, f.daemonNode)
}

func pingAt(l *serverkit.Listener, service, token string) error {
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, "http://"+l.Addr().String()+"/"+service+"/Ping")
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := client.CallUnary(context.Background(), req)
	return err
}

func TestFixtureMain_TwoListenersUnderSupervise(t *testing.T) {
	registerFixtureServices(t)
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	booted := make(chan *fixtureListeners, 1)
	done := make(chan error, 1)
	go func() { done <- fixtureMain(ctx, logger, booted, daemonService) }()
	f := <-booted
	waitFor(t, "the process to be ready", f.drainer.Ready)

	if err := pingAt(f.api, userService, "user"); err != nil {
		t.Fatalf("user Ping on the API listener: %v", err)
	}
	if err := pingAt(f.daemon, daemonService, "daemon"); err != nil {
		t.Fatalf("daemon Ping on the daemon listener: %v", err)
	}
	if err := pingAt(f.daemon, daemonService, "user"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a user token on the daemon listener: %v, want Unauthenticated", err)
	}
	resp, err := http.Get("http://" + f.api.Addr().String() + "/readyz") //nolint:noctx // test probe
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz on the API listener: %v %v", resp, err)
	}
	_ = resp.Body.Close()

	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("fixture main: %v", err)
	}
	if f.api.Ready(context.Background()) == nil || f.daemon.Ready(context.Background()) == nil {
		t.Fatal("a listener still reports ready after the stop")
	}
	out := logs.String()
	daemonLine, apiLine := strings.Index(out, "node=daemon"), strings.Index(out, "node=api")
	if daemonLine < 0 || apiLine < 0 || daemonLine > apiLine {
		t.Fatalf("want the daemon listener stopped before the API listener:\n%s", out)
	}
	if n := strings.Count(out, "drain: listener drained"); n != 2 {
		t.Fatalf("%d listener drain lines, want one per listener:\n%s", n, out)
	}
}

// Mounting a user service on the daemon listener fails at boot: the fixture
// main returns the audience error before Supervise starts anything.
func TestFixtureMain_UserServiceOnDaemonListenerFailsAtBoot(t *testing.T) {
	registerFixtureServices(t)
	booted := make(chan *fixtureListeners, 1)
	err := fixtureMain(context.Background(), slog.New(slog.DiscardHandler), booted, daemonService, userService)
	if err == nil || !strings.Contains(err.Error(), userService+` (audience "user")`) {
		t.Fatalf("fixture main = %v, want the audience check naming %s", err, userService)
	}
	if len(booted) != 0 {
		t.Fatal("the fixture main booted with a user service on the daemon listener")
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
