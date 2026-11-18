// Copyright (c) 2026 Reliant Labs
package background

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// reconnectingCommandRouter answers every daemon command the way
// NATSDaemonRouter does when the gateway held it for a freshly registered
// stream that never answered, and released it unsent.
type reconnectingCommandRouter struct {
	toolexectest.Stub
}

func (*reconnectingCommandRouter) SendDaemonCommand(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
	return nil, fmt.Errorf("daemon command %q: %w", commandType, toolexec.ErrDaemonReconnecting)
}

// Issue fe2d2455 (prod, 2026-11-17): a laptop waking from sleep re-registered
// its stream, the gateway answered exec.bg_list ErrDaemonReconnecting, and
// ListProcesses mapped that Unavailable to Internal: "rpc failed" at ERROR,
// and no reason for the web to wait on. It is Unavailable with the
// daemon_reconnecting reason, a user error logged at INFO. A genuine daemon
// failure on the same path is still Internal and still an ERROR.
func TestBackgroundProxy_ReconnectingMachineIsUnavailableNotInternal(t *testing.T) {
	for name, tc := range map[string]struct {
		router     toolexec.DaemonRouter
		wantCode   connect.Code
		wantReason string
		wantServer bool
		wantLevel  slog.Level
	}{
		"machine reconnecting": {
			router:     &reconnectingCommandRouter{},
			wantCode:   connect.CodeUnavailable,
			wantReason: errreason.DaemonReconnecting,
			wantLevel:  slog.LevelInfo,
		},
		"genuine daemon failure": {
			router:     &reportingCommandRouter{text: "process table unreadable: permission denied"},
			wantCode:   connect.CodeInternal,
			wantServer: true,
			wantLevel:  slog.LevelError,
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: tc.router}))
			rec := &levelRecorder{}
			// As internal/grpc/user_chain.go wires the RPC edge.
			handler := observe.LoggingInterceptor(slog.New(rec), observe.WithErrorClassifier(errclass.Library)).WrapUnary(
				func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					return svc.ListProcesses(ctx, req.(*connect.Request[reliantv1.ListBackgroundProcessesRequest]))
				})

			_, err := handler(AuthedContext(t), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{}))
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, svcerr.Code(err))
			assert.Equal(t, tc.wantReason, svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader))
			assert.Equal(t, tc.wantServer, errclass.IsServerError(err))
			assert.Equal(t, []slog.Level{tc.wantLevel}, rec.levels)
		})
	}
}
