// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

// passAuth is an authenticator that admits every call unchanged.
func passAuth(next connect.ServerFunc) connect.ServerFunc { return next }

// unarySpec is the spec of procedure as a unary rpc of Empty to Empty.
func unarySpec(procedure string) connect.Spec {
	return connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: procedure}
}

// serveUnary serves procedure, a unary rpc of Empty to Empty that handle
// answers, behind interceptors, and returns the server's base URL.
func serveUnary(t testing.TB, procedure string, handle func(ctx context.Context) error, interceptors ...connect.ServerInterceptor) string {
	t.Helper()
	server := connect.NewServer(interceptors...)
	server.Register(connect.Method{
		Spec: unarySpec(procedure),
		Handler: func(ctx context.Context, _ connect.Spec, stream connect.ServerStream) error {
			if err := stream.Receive(&emptypb.Empty{}); err != nil {
				return err
			}
			if err := handle(ctx); err != nil {
				return err
			}
			return stream.Send(&emptypb.Empty{})
		},
	})
	return mount(t, server)
}

// serveServerStream serves procedure, a server-streaming rpc of Empty to
// Empty that handle answers once it has the request, behind interceptors,
// and returns the server's base URL.
func serveServerStream(t testing.TB, procedure string, handle func(ctx context.Context, stream connect.ServerStream) error, interceptors ...connect.ServerInterceptor) string {
	t.Helper()
	server := connect.NewServer(interceptors...)
	server.Register(connect.Method{
		Spec: connect.Spec{StreamType: connect.StreamTypeServer, Procedure: procedure},
		Handler: func(ctx context.Context, _ connect.Spec, stream connect.ServerStream) error {
			if err := stream.Receive(&emptypb.Empty{}); err != nil {
				return err
			}
			return handle(ctx, stream)
		},
	})
	return mount(t, server)
}

func mount(t testing.TB, server *connect.Server) string {
	t.Helper()
	mux := http.NewServeMux()
	mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// callUnary calls procedure on baseURL with an Empty request over hc.
func callUnary(ctx context.Context, hc *http.Client, baseURL, procedure string, interceptors ...connect.ClientInterceptor) error {
	return rpcclient.New(hc, baseURL, nil, interceptors...).CallUnary(ctx, unarySpec(procedure), &emptypb.Empty{}, &emptypb.Empty{})
}
