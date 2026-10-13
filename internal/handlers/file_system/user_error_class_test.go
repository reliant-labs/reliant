// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestFsProxyDaemonError_MachineStateIsAUserError(t *testing.T) {
	for name, err := range map[string]error{
		"starting":   fmt.Errorf("resolving daemon for command: your machine is still starting: %w", toolexec.ErrDaemonPending),
		"no machine": fmt.Errorf("resolving daemon for command: %w", toolexec.ErrNoDaemon),
	} {
		mapped := fsProxyDaemonError(err)
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(mapped), name)
	}
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(fsProxyDaemonError(errors.New("decode tree: unexpected EOF"))))
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

// A machine that is connected but answers after the deadline is a server
// condition worth seeing, not a page. Prod, 2026-10-10: a saturated cloud
// machine's file-tree polls timed out, the proxy answered Internal, forge
// logged "rpc failed" at ERROR, and the Sentry bridge forwards ERROR — the
// "[gRPC] Request error" page. Mapped to DeadlineExceeded, the same failure
// is a WARN, which never reaches Sentry.
func TestFsProxyDaemonError_TimeoutLogsAtWarnNotError(t *testing.T) {
	timeout := fmt.Errorf("daemon command fs.get_tree (subject s) via NATS failed: %w: %w",
		toolexec.ErrDaemonTimeout, context.DeadlineExceeded)
	mapped := fsProxyDaemonError(timeout)
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(mapped))
	assert.Equal(t, svcerr.ClassServer, svcerr.Classify(mapped), "still a server condition, still logged")

	rec := &levelRecorder{}
	handler := observe.LoggingInterceptor(slog.New(rec)).WrapUnary(
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, mapped })
	_, err := handler(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.Error(t, err)
	assert.Equal(t, []slog.Level{slog.LevelWarn}, rec.levels)
}
