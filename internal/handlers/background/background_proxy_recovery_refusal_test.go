// Copyright (c) 2026 Reliant Labs
package background

import (
	"context"
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

// reportingCommandRouter answers every daemon command with the daemon's own
// failure text, as NATSDaemonRouter does for a daemon that answered failure.
type reportingCommandRouter struct {
	toolexectest.Stub
	text string
}

func (r *reportingCommandRouter) SendDaemonCommand(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
	return nil, toolexec.DaemonReportedFailure(commandType, r.text)
}

// levelRecorder keeps the level of every record logged through it.
type levelRecorder struct{ levels []slog.Level }

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.levels = append(r.levels, rec.Level)
	return nil
}
func (r *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *levelRecorder) WithGroup(string) slog.Handler      { return r }

// The prod line of reliant#2429: ListProcesses polled exec.bg_list while the
// daemon finalized a worktree's recovery, answered Internal, and the RPC edge
// logged "rpc failed" at ERROR ~170 times for a refusal every caller retries.
// It logs at INFO, and answers Unavailable with the worktree_recovering
// reason (the web reported each Internal poll as a bug: prod 1ada7020), in
// the daemon's words. A genuine daemon failure on the same path stays an
// Internal ERROR.
func TestBackgroundProxy_RecoveryRefusalLogsBelowError(t *testing.T) {
	for name, tc := range map[string]struct {
		text       string
		wantLevel  slog.Level
		wantCode   connect.Code
		wantReason string
	}{
		"recovery finalizing": {text: toolexec.ErrRecoveryFinalizing.Error(), wantLevel: slog.LevelInfo,
			wantCode: connect.CodeUnavailable, wantReason: errreason.WorktreeRecovering},
		"genuine failure": {text: "process table unreadable: permission denied", wantLevel: slog.LevelError,
			wantCode: connect.CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: &reportingCommandRouter{text: tc.text}}))
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
			assert.Contains(t, svcerr.ToConnect(err).Message(), tc.text, "the daemon's words reach the user")
			assert.Equal(t, []slog.Level{tc.wantLevel}, rec.levels)
		})
	}
}
