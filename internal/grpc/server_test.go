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

	result := newInterceptors(true, timeout, auth)
	// Forge's five middleware slots stay stable. otelconnect is the first extra
	// and owns the only Connect server span; the remaining extras are Reliant's.
	require.Len(t, result, 10)
	require.IsType(t, &interceptors.ErrorReporterInterceptor{}, result[7])
	require.Same(t, timeout, result[8])
	require.Same(t, auth, result[9])
}

func TestNewInterceptorsSkipsNilAuthInterceptor(t *testing.T) {
	timeout := &testNamedInterceptor{}

	result := newInterceptors(true, timeout, (*interceptors.AuthInterceptor)(nil))
	// Forge's five + otelconnect + Reliant's three extras = 9 (nil auth skipped).
	require.Len(t, result, 9)
	require.IsType(t, &interceptors.ErrorReporterInterceptor{}, result[7])
	require.Same(t, timeout, result[8])
}
