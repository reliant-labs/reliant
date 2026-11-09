package serverkit_test

import (
	"context"
	"errors"
	"net/http"
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
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// The fixture for the composition tests: two proto services in the global
// registry, one serving users (no audience option — the default) and one
// declaring `option (forge.v1.service) = {audience: "daemon"}`.
const (
	fixturePkg           = "serverkit.composition.v1"
	fixtureUserService   = fixturePkg + ".UserService"
	fixtureDaemonService = fixturePkg + ".DaemonService"
	audienceDaemon       = "daemon"
)

var registerCompositionFile sync.Once

func registerAudienceServices(t *testing.T) {
	t.Helper()
	registerCompositionFile.Do(func() {
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
		fdp := &descriptorpb.FileDescriptorProto{
			Name:       proto.String("serverkit_composition.proto"),
			Package:    proto.String(fixturePkg),
			Syntax:     proto.String("proto3"),
			Dependency: []string{"google/protobuf/empty.proto", "forge/v1/forge.proto"},
			Service: []*descriptorpb.ServiceDescriptorProto{
				svc("UserService", nil),
				svc("DaemonService", daemonOpts),
			},
		}
		fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
		if err != nil {
			t.Fatalf("protodesc.NewFile: %v", err)
		}
		if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
			t.Fatalf("RegisterFile: %v", err)
		}
	})
}

// pingProcedure is the Connect path of a fixture service's Ping.
func pingProcedure(service string) string { return "/" + service + "/Ping" }

// pingRoutes mounts Ping for each service, built with the listener's chain.
func pingRoutes(services ...string) serverkit.RoutesFunc {
	return func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
		for _, s := range services {
			p := pingProcedure(s)
			mux.Handle(p, connect.NewUnaryHandler(p,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					return connect.NewResponse(&emptypb.Empty{}), nil
				}, opts...))
		}
		return services
	}
}

// tokenAuth admits a request whose Authorization header is "Bearer <token>".
type tokenAuth struct{ token string }

// Claims names forge authn's claims reader, as authn's own interceptor does, so
// AuthFor gives this fixture a Caller and the chain's default Audit and
// Activity slots build.
func (tokenAuth) Claims() func(context.Context) (*authn.Claims, bool) { return authn.ClaimsFromContext }

func (a tokenAuth) check(h http.Header) error {
	if h.Get("Authorization") != "Bearer "+a.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("wrong credential"))
	}
	return nil
}

func (a tokenAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.check(req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a tokenAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a tokenAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.check(conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// chainFor builds a chain admitting audience with token as its credential.
func chainFor(t *testing.T, audience, token string) *serverkit.Chain {
	t.Helper()
	c, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor(audience, tokenAuth{token: token})})
	if err != nil {
		t.Fatalf("NewChain(%s): %v", audience, err)
	}
	return c
}

// ping calls service's Ping on base with token.
func ping(base, service, token string) error {
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, base+pingProcedure(service))
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := client.CallUnary(context.Background(), req)
	return err
}
