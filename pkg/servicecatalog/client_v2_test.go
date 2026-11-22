package servicecatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

var pingSpecV2 = connectv2.Spec{Procedure: "/demo.v1.API/Ping", StreamType: connectv2.StreamTypeUnary}

// generatedClientV2 is the shape protoc-gen-connect-go v2 generates: a client
// interface over bare messages, built from one *connect.Client.
type generatedClientV2 interface {
	Ping(context.Context, *emptypb.Empty) (*wrapperspb.StringValue, error)
}

type clientV2 struct{ c *connectv2.Client }

func (c *clientV2) Ping(ctx context.Context, req *emptypb.Empty) (*wrapperspb.StringValue, error) {
	res := &wrapperspb.StringValue{}
	if err := c.c.CallUnary(ctx, pingSpecV2, req, res); err != nil {
		return nil, err
	}
	return res, nil
}

func constructorV2(c *connectv2.Client) generatedClientV2 { return &clientV2{c} }

// Mirrors TestNewClientPreservesGeneratedInterfaceHTTPClientAndOptions: the
// generated interface, the caller's HTTP client, its transport options and
// its interceptors in order all reach the call.
func TestNewClientV2PreservesGeneratedInterfaceHTTPClientAndOptions(t *testing.T) {
	calls := 0
	var payload string
	srv := connectv2.NewServer()
	srv.Register(connectv2.Method{Spec: pingSpecV2, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		if err := stream.Receive(&emptypb.Empty{}); err != nil {
			return err
		}
		info, _ := connectv2.CallInfoForServerContext(ctx)
		if info.RequestHeader().Get("Authorization") != "Bearer app-owned" || info.RequestHeader().Get("X-Order") != "first,second" {
			t.Errorf("auth/interceptor order lost: %v", info.RequestHeader().Get("X-Order"))
		}
		if info.Codec != "json" {
			t.Errorf("codec = %q: the caller's transport options were dropped", info.Codec)
		}
		calls++
		return stream.Send(wrapperspb.String(payload))
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	server := httptest.NewServer(mux)
	defer server.Close()

	h := &recordingHTTP{}
	interceptor := func(word string) connectv2.ClientInterceptor {
		return func(next connectv2.ClientFunc) connectv2.ClientFunc {
			return func(ctx context.Context, spec connectv2.Spec) (connectv2.ClientStream, error) {
				info, _ := connectv2.CallInfoForClientContext(ctx)
				info.RequestHeader().Set("Authorization", "Bearer app-owned")
				prior := info.RequestHeader().Get("X-Order")
				if prior != "" {
					prior += ","
				}
				info.RequestHeader().Set("X-Order", prior+word)
				return next(ctx, spec)
			}
		}
	}
	var c generatedClientV2 = NewClientV2(ClientBinding{Service: "demo.v1.API", ConfigField: "demo.config.API.url"},
		constructorV2, h, server.URL, []connecthttp.Option{connecthttp.WithProtoJSON()}, interceptor("first"), interceptor("second"))

	// A response over connect-go v2's 4 MiB default: NewClient (v1) reads any
	// size, so NewClientV2 does too.
	payload = strings.Repeat("x", 5<<20)
	res, err := c.Ping(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetValue()) != len(payload) {
		t.Fatalf("response = %d bytes, want %d", len(res.GetValue()), len(payload))
	}
	if calls != 1 || h.requests != 1 {
		t.Fatalf("custom HTTPClient not preserved: %d %d", calls, h.requests)
	}
}

// NewClientV2's client reads the reason a server refused for from the error
// alone (svcerr.MetaOf), as a v1 client read ce.Meta().
func TestNewClientV2ErrorCarriesServerMetadata(t *testing.T) {
	srv := connectv2.NewServer(svcerr.InterceptorV2())
	srv.Register(connectv2.Method{Spec: pingSpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		if err := stream.Receive(&emptypb.Empty{}); err != nil {
			return err
		}
		return svcerr.WithReason(svcerr.PermissionDenied("not yours"), "not_owner")
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewClientV2(ClientBinding{Service: "demo.v1.API", ConfigField: "demo.config.API.url"},
		constructorV2, server.Client(), server.URL, nil)
	_, err := c.Ping(context.Background(), &emptypb.Empty{})
	if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "not_owner" {
		t.Fatalf("reason = %q, want not_owner (err %v)", got, err)
	}
}
