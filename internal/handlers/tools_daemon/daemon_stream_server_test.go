// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/rpcclient"
)

// testUserHeader names the user a test's daemon connects as; withTestUser
// installs it on the handler's ctx as the daemon auth interceptor would.
const testUserHeader = "X-Test-User"

// daemonServer serves svc as production mounts it — svc.RegisterV2 on a
// connect server whose chain ends with svcerr.InterceptorV2 (the end of
// serverkit's chain: a handler's svcerr error keeps its code on the wire),
// then interceptors — with edge around the mux, over h2c. abortable puts the
// listener's edge outermost (serverkit.Streams.Install): the abortable request
// body ConnectDaemon needs (serverkit.AbortReceive). It returns a client
// speaking gRPC, as a daemon does. Everything is torn down at test end.
func daemonServer(t *testing.T, svc *Service, abortable bool, edge func(http.Handler) http.Handler, interceptors ...connect.ServerInterceptor) reliantv1connect.ToolsDaemonServiceClient {
	t.Helper()
	server := connect.NewServer(append([]connect.ServerInterceptor{svcerr.InterceptorV2()}, interceptors...)...)
	svc.RegisterV2(server)
	return serveDaemonH2C(t, server, abortable, edge)
}

// serveDaemonH2C serves server over h2c behind edge (and, when abortable,
// serverkit.Streams.Install) and returns a gRPC client for it.
func serveDaemonH2C(t *testing.T, server *connect.Server, abortable bool, edge func(http.Handler) http.Handler) reliantv1connect.ToolsDaemonServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mountkit.MountV2(mux, server, connecthttp.WithReadMaxBytes(0))
	var handler http.Handler = mux
	if edge != nil {
		handler = edge(handler)
	}
	if abortable {
		handler = serverkit.NewStreams().Install(handler)
	}
	srv := httptest.NewUnstartedServer(h2c.NewHandler(handler, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)

	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	return reliantv1connect.NewToolsDaemonServiceClient(rpcclient.New(cl, srv.URL, []connecthttp.Option{connecthttp.WithGRPC()}))
}

// withTestUser authenticates each request as the user its testUserHeader
// names, as the daemon auth interceptor puts the credential's user on ctx.
func withTestUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, r.Header.Get(testUserHeader))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// asTestUser is ctx for a call whose testUserHeader names userID, plus any
// further request headers as key, value pairs.
func asTestUser(ctx context.Context, userID string, headers ...string) context.Context {
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set(testUserHeader, userID)
	for i := 0; i+1 < len(headers); i += 2 {
		info.RequestHeader().Set(headers[i], headers[i+1])
	}
	return ctx
}
