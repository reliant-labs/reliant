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

func TestNewInterceptorsOrdersRecoveryErrorReporterTimeoutAndAuth(t *testing.T) {
	timeout := &testNamedInterceptor{}
	auth := &testNamedInterceptor{}

	result := newInterceptors(timeout, auth)
	require.Len(t, result, 4)
	require.IsType(t, &interceptors.RecoveryInterceptor{}, result[0])
	require.IsType(t, &interceptors.ErrorReporterInterceptor{}, result[1])
	require.Same(t, timeout, result[2])
	require.Same(t, auth, result[3])
}

func TestNewInterceptorsSkipsNilAuthInterceptor(t *testing.T) {
	timeout := &testNamedInterceptor{}

	result := newInterceptors(timeout, (*interceptors.AuthInterceptor)(nil))
	require.Len(t, result, 3)
	require.IsType(t, &interceptors.RecoveryInterceptor{}, result[0])
	require.IsType(t, &interceptors.ErrorReporterInterceptor{}, result[1])
	require.Same(t, timeout, result[2])
}
