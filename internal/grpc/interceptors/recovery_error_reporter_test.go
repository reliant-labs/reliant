// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/stretchr/testify/require"
)

type telemetrySpyReporter struct {
	errors []error
}

func (r *telemetrySpyReporter) CaptureException(err error) string {
	r.errors = append(r.errors, err)
	return "captured"
}

func (r *telemetrySpyReporter) CaptureMessage(string) string { return "" }
func (r *telemetrySpyReporter) SetUser(string, string)       {}
func (r *telemetrySpyReporter) SetTag(string, string)        {}
func (r *telemetrySpyReporter) Flush(int) bool               { return true }

type fakeStreamingHandlerConn struct {
	spec connect.Spec
}

func (c *fakeStreamingHandlerConn) Spec() connect.Spec           { return c.spec }
func (c *fakeStreamingHandlerConn) Peer() connect.Peer           { return connect.Peer{} }
func (c *fakeStreamingHandlerConn) Receive(any) error            { return nil }
func (c *fakeStreamingHandlerConn) RequestHeader() http.Header   { return make(http.Header) }
func (c *fakeStreamingHandlerConn) Send(any) error               { return nil }
func (c *fakeStreamingHandlerConn) ResponseHeader() http.Header  { return make(http.Header) }
func (c *fakeStreamingHandlerConn) ResponseTrailer() http.Header { return make(http.Header) }

func withTelemetryReporter(t *testing.T, reporter telemetry.ErrorReporter) {
	t.Helper()
	telemetry.SetReporter(reporter)
	t.Cleanup(func() { telemetry.SetReporter(nil) })
}

func TestRecoveryInterceptorWrapUnaryRecoversPanicAsInternal(t *testing.T) {
	spy := &telemetrySpyReporter{}
	withTelemetryReporter(t, spy)

	wrapped := NewRecoveryInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		panic("boom")
	})

	resp, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Len(t, spy.errors, 1)
	require.Contains(t, spy.errors[0].Error(), "panic in")
}

func TestRecoveryInterceptorWrapStreamingHandlerRecoversPanicAsInternal(t *testing.T) {
	spy := &telemetrySpyReporter{}
	withTelemetryReporter(t, spy)

	wrapped := NewRecoveryInterceptor().WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		panic("stream boom")
	})

	err := wrapped(context.Background(), &fakeStreamingHandlerConn{
		spec: connect.Spec{Procedure: "/reliant.v1.TestService/Stream"},
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Len(t, spy.errors, 1)
	require.Contains(t, spy.errors[0].Error(), "panic in streaming /reliant.v1.TestService/Stream")
}

func TestErrorReporterInterceptorSkipsExpectedAndTransientErrors(t *testing.T) {
	interceptor := NewErrorReporterInterceptor()

	t.Run("expected connect code", func(t *testing.T) {
		spy := &telemetrySpyReporter{}
		withTelemetryReporter(t, spy)

		wrapped := interceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad request"))
		})

		_, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		require.Empty(t, spy.errors)
	})

	t.Run("transient internal error message", func(t *testing.T) {
		spy := &telemetrySpyReporter{}
		withTelemetryReporter(t, spy)

		wrapped := interceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeInternal, errors.New("write tcp: broken pipe"))
		})

		_, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		require.Empty(t, spy.errors)
	})
}

func TestErrorReporterInterceptorReportsUnexpectedInternalErrors(t *testing.T) {
	spy := &telemetrySpyReporter{}
	withTelemetryReporter(t, spy)

	wrapped := NewErrorReporterInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("database corruption detected"))
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Len(t, spy.errors, 1)
	require.Contains(t, spy.errors[0].Error(), "database corruption detected")
}

func TestInterceptorsAreSafeWithNoopReporter(t *testing.T) {
	withTelemetryReporter(t, telemetry.NewNoopReporter())

	recoveryWrapped := NewRecoveryInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		panic("noop panic")
	})
	resp, err := recoveryWrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	errorWrapped := NewErrorReporterInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("unexpected internal error"))
	})
	resp, err = errorWrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}
