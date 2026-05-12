// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/stretchr/testify/require"
)

type testNamedInterceptor struct{}

func (i *testNamedInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(ctx, req)
	}
}
func (i *testNamedInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (i *testNamedInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, conn)
	}
}

func TestNewInterceptorsUsesForgeChainWithExtras(t *testing.T) {
	timeout := &testNamedInterceptor{}
	auth := &testNamedInterceptor{}

	result := newInterceptors(timeout, auth)
	// forge's DefaultMiddlewares produces 5 (Recovery, RequestID, Logging, Tracing, Metrics)
	// + Extras: ErrorReporterInterceptor, timeout, auth = 8 total
	require.Len(t, result, 8)
	// The last three are the reliant-specific Extras in order.
	require.IsType(t, &interceptors.ErrorReporterInterceptor{}, result[5])
	require.Same(t, timeout, result[6])
	require.Same(t, auth, result[7])
}

func TestNewInterceptorsSkipsNilAuthInterceptor(t *testing.T) {
	timeout := &testNamedInterceptor{}

	result := newInterceptors(timeout, (*interceptors.AuthInterceptor)(nil))
	// forge's 5 + ErrorReporterInterceptor + timeout = 7 (nil auth skipped)
	require.Len(t, result, 7)
	require.IsType(t, &interceptors.ErrorReporterInterceptor{}, result[5])
	require.Same(t, timeout, result[6])
}
