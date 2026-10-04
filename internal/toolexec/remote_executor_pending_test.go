// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingRouter fails every daemon call with a wrapped ErrDaemonPending, the
// way NATSDaemonRouter does for a suspended machine.
type pendingRouter struct {
	routerStub
	err error
}

func (r *pendingRouter) SendToolRequestSync(context.Context, string, *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	return nil, r.err
}

// ErrDaemonPending is flattened into a ToolResult's text at this boundary. The
// typed fact must survive it, so the run's state is set without anyone parsing
// the message.
func TestExecuteOnDaemon_CarriesDaemonPendingAcrossTheResultBoundary(t *testing.T) {
	req := &ToolRequest{UserID: "u", ChatID: "c", ProjectID: "p", ToolName: "bash", ToolCallID: "tc"}

	t.Run("suspended machine", func(t *testing.T) {
		executor := &RemoteExecutor{router: &pendingRouter{err: fmt.Errorf("the machine is suspended: %w", ErrDaemonPending)}}
		res, err := executor.executeOnDaemon(context.Background(), req, time.Now())
		require.NoError(t, err)
		assert.True(t, res.DaemonPending)
		assert.False(t, res.RanOnDaemon)
		assert.True(t, IsTransportErrorCode(res.ErrorCode), "still reported as a transport failure")
	})

	t.Run("other transport failure is not pending", func(t *testing.T) {
		executor := &RemoteExecutor{router: &pendingRouter{err: fmt.Errorf("nats: timeout")}}
		res, err := executor.executeOnDaemon(context.Background(), req, time.Now())
		require.NoError(t, err)
		assert.False(t, res.DaemonPending)
		assert.False(t, res.RanOnDaemon)
	})

	t.Run("a completed round trip proves the machine is reachable", func(t *testing.T) {
		executor := &RemoteExecutor{router: &routerStub{}}
		res, err := executor.executeOnDaemon(context.Background(), req, time.Now())
		require.NoError(t, err)
		assert.False(t, res.DaemonPending)
		assert.True(t, res.RanOnDaemon)
	})
}
