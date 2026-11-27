package testkit_test

import (
	"context"
	"errors"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The v2 twins of principalRegister / kindRegister: hand-built v2 methods,
// what a v2 generated Register<Svc>Handler registers.

var (
	principalSpecV2  = connectv2.Spec{Procedure: principalProcedure, StreamType: connectv2.StreamTypeUnary}
	kindSpecV2       = connectv2.Spec{Procedure: kindProcedure, StreamType: connectv2.StreamTypeUnary}
	kindStreamSpecV2 = connectv2.Spec{Procedure: kindStreamProcedure, StreamType: connectv2.StreamTypeServer}
)

func principalRegisterV2(server *connectv2.Server) {
	server.Register(connectv2.Method{Spec: principalSpecV2, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		var req structpb.Struct
		if err := stream.Receive(&req); err != nil {
			return err
		}
		claims, ok := localClaimsFrom(ctx)
		if !ok || claims == nil {
			return connectv2.NewError(connectv2.CodeUnauthenticated, "no principal")
		}
		return stream.Send(&structpb.Struct{Fields: map[string]*structpb.Value{
			"user_id": structpb.NewStringValue(claims.UserID),
			"role":    structpb.NewStringValue(claims.Role),
		}})
	}})
}

func kindRegisterV2(server *connectv2.Server) {
	server.Register(
		connectv2.Method{Spec: kindSpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req structpb.Struct
			if err := stream.Receive(&req); err != nil {
				return err
			}
			return svcerr.InvalidArgument("name is required")
		}},
		connectv2.Method{Spec: kindStreamSpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req structpb.Struct
			if err := stream.Receive(&req); err != nil {
				return err
			}
			return svcerr.NotFound("process")
		}},
	)
}

func clientV2(c *connectv2.Client) *connectv2.Client { return c }

func whoAmIV2(t *testing.T, ctx context.Context, c *connectv2.Client) (*structpb.Struct, error) {
	t.Helper()
	var res structpb.Struct
	err := c.CallUnary(ctx, principalSpecV2, &structpb.Struct{}, &res)
	return &res, err
}

// ServerPrincipalV2 installs the principal where the v2 handler runs; a
// client-side AuthedContext does not cross the wire.
func TestServerPrincipalV2_ReachesTheHandlerOverTheWire(t *testing.T) {
	t.Parallel()
	_, bare := testkit.NewConnectClientV2(t, principalRegisterV2, clientV2)
	if _, err := whoAmIV2(t, testkit.AuthedContext(t, localWithClaims), bare); connectv2.CodeOf(err) != connectv2.CodeUnauthenticated {
		t.Fatalf("a bare server must not see a client-side principal; got err=%v", err)
	}

	_, authed := testkit.NewConnectClientV2(t, principalRegisterV2, clientV2,
		testkit.ServerPrincipalV2(localWithClaims, testkit.WithUserID("u-42"), testkit.WithRoles("viewer")))
	resp, err := whoAmIV2(t, context.Background(), authed)
	if err != nil {
		t.Fatalf("handler behind ServerPrincipalV2 saw no principal: %v", err)
	}
	if got := resp.GetFields()["user_id"].GetStringValue(); got != "u-42" {
		t.Fatalf("user_id = %q, want %q", got, "u-42")
	}
	if got := resp.GetFields()["role"].GetStringValue(); got != "viewer" {
		t.Fatalf("role = %q, want %q", got, "viewer")
	}
}

// One default principal for both majors and AuthedContext.
func TestServerPrincipalV2_DefaultsMatchAuthedContext(t *testing.T) {
	t.Parallel()
	_, client := testkit.NewConnectClientV2(t, principalRegisterV2, clientV2, testkit.ServerPrincipalV2(localWithClaims))
	resp, err := whoAmIV2(t, context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := localClaimsFrom(testkit.AuthedContext(t, localWithClaims))
	if got := resp.GetFields()["user_id"].GetStringValue(); got != want.UserID {
		t.Fatalf("user_id = %q, want %q", got, want.UserID)
	}
	if got := resp.GetFields()["role"].GetStringValue(); got != want.Role {
		t.Fatalf("role = %q, want %q", got, want.Role)
	}
}

// The v2 test server ends its chain with svcerr.InterceptorV2: a bare svcerr
// kind reaches the client with its code and message, unary and streaming —
// a v2 transport would otherwise send it as a bare unknown.
func TestNewConnectClientV2_ServesBehindSvcerrInterceptor(t *testing.T) {
	t.Parallel()
	_, client := testkit.NewConnectClientV2(t, kindRegisterV2, clientV2)

	err := client.CallUnary(context.Background(), kindSpecV2, &structpb.Struct{}, &structpb.Struct{})
	var ce *connectv2.Error
	if !errors.As(err, &ce) || ce.Code() != connectv2.CodeInvalidArgument || ce.Message() != "name is required" {
		t.Fatalf("unary: client read %v, want invalid_argument \"name is required\"", err)
	}

	stream, err := client.CallServerStream(context.Background(), kindStreamSpecV2, &structpb.Struct{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if err := stream.Receive(&structpb.Struct{}); connectv2.CodeOf(err) != connectv2.CodeNotFound {
		t.Fatalf("stream: client read %v, want not_found", err)
	}
}
