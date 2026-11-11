package toolexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowMCPRouter answers mcp.ensure_loaded only when told to, the way an
// overloaded machine did in prod: not at all inside the caller's timeout.
type slowMCPRouter struct {
	routerStub
	release chan struct{}
	failure error

	mu       sync.Mutex
	commands map[string]int
}

func (r *slowMCPRouter) SendDaemonCommand(ctx context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	if r.commands == nil {
		r.commands = map[string]int{}
	}
	r.commands[commandType]++
	r.mu.Unlock()
	switch commandType {
	case "mcp.ensure_loaded":
		if r.release != nil {
			select {
			case <-r.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if r.failure != nil {
			return nil, r.failure
		}
		return []byte(`{"loaded_servers":["docs"],"failed_servers":[]}`), nil
	case "mcp.server_status":
		return []byte(`{"servers":[]}`), nil
	}
	return nil, nil
}

func (r *slowMCPRouter) sent(commandType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commands[commandType]
}

func shortMCPLoadBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	prev := mcpLoadWaitBudget
	mcpLoadWaitBudget = budget
	t.Cleanup(func() { mcpLoadWaitBudget = prev })
}

func bindSlowMCPRuntime(t *testing.T, binder MCPContextBinder) *rctx.ToolContext {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-1")
	bound := binder.Bind(rctx.NewToolContext(ctx, "chat", "thread", nil, nil))
	require.NotNil(t, bound.MCP)
	return bound
}

// A turn never waits on a slow machine longer than the budget, never asks it
// to load again while it is still working, and does not wait on its listing
// either. The load still lands, and the next turn sees its result.
func TestDaemonMCPRuntime_SlowLoadIsBoundedAndContinuesInBackground(t *testing.T) {
	shortMCPLoadBudget(t, 50*time.Millisecond)
	router := &slowMCPRouter{release: make(chan struct{})}
	binder := NewDaemonMCPContextBinder(router)
	turn := func() *rctx.ToolContext { return bindSlowMCPRuntime(t, binder) }

	start := time.Now()
	result := turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
	assert.Less(t, time.Since(start), 30*time.Second, "the turn waited on the machine past its budget")
	require.True(t, result.HasFailures(), "an unfinished load cannot report its servers as loaded")

	time.Sleep(60 * time.Millisecond) // the load has now outrun the budget
	// errMCPMachineUnresponsive comes only from the path that does not wait;
	// waiting out the budget again would report the load as still running.
	result = turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
	assert.ErrorIs(t, result.Errors["/proj"], errMCPMachineUnresponsive, "a machine still working through a load must not be waited on again")
	_, err := turn().MCP.ListAllTools()
	require.ErrorIs(t, err, errMCPMachineUnresponsive, "the listing would wait out its own timeout on the same machine")
	assert.Equal(t, 1, router.sent("mcp.ensure_loaded"), "one load in flight per machine and project")
	assert.Zero(t, router.sent("mcp.server_status"))

	close(router.release)
	require.Eventually(t, func() bool {
		r := turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
		return !r.HasFailures() && len(r.LoadedServers) == 1
	}, 30*time.Second, 10*time.Millisecond, "once the machine answers, the next turn loads normally")
}

// A machine that failed to answer is left alone for the cooldown: the next
// turn neither re-sends the command nor waits on anything.
func TestDaemonMCPRuntime_FailedMachineIsLeftAloneForTheCooldown(t *testing.T) {
	shortMCPLoadBudget(t, time.Second)
	router := &slowMCPRouter{failure: errors.New("nats: timeout")}
	binder := NewDaemonMCPContextBinder(router)
	turn := func() *rctx.ToolContext { return bindSlowMCPRuntime(t, binder) }

	result := turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
	require.True(t, result.HasFailures())
	require.ErrorContains(t, result.Errors["/proj"], "nats: timeout")

	result = turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
	assert.ErrorIs(t, result.Errors["/proj"], errMCPMachineUnresponsive)
	_, err := turn().MCP.ListProjectTools("/proj")
	require.ErrorIs(t, err, errMCPMachineUnresponsive)
	assert.Equal(t, 1, router.sent("mcp.ensure_loaded"))
	assert.Zero(t, router.sent("mcp.server_status"))
}

// A machine that is not connected fails at once and is not put in the
// cooldown: the first turn after it reconnects gets its MCP servers, instead of
// running without them for up to two minutes.
func TestDaemonMCPRuntime_AbsentMachineGetsItsServersOnReconnect(t *testing.T) {
	for name, absent := range map[string]error{
		"suspended":     machineState("machine m1 is suspended", ErrDaemonSuspended),
		"not connected": connect.NewError(connect.CodeUnavailable, errDaemonNotConnected),
		"reconnecting":  notDispatched(ErrDaemonReconnecting, "m1"),
		"no machine":    noMachine("none registered"),
	} {
		t.Run(name, func(t *testing.T) {
			shortMCPLoadBudget(t, time.Second)
			router := &slowMCPRouter{failure: absent}
			binder := NewDaemonMCPContextBinder(router)
			turn := func() *rctx.ToolContext { return bindSlowMCPRuntime(t, binder) }

			result := turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
			require.True(t, result.HasFailures())
			require.NotErrorIs(t, result.Errors["/proj"], errMCPMachineUnresponsive)

			router.failure = nil // the machine is back
			result = turn().MCP.EnsureProjectServersLoaded(turn().Context, "/proj")
			assert.False(t, result.HasFailures(), "the reconnected machine was left in the cooldown: %v", result.Errors)
			assert.Equal(t, []string{"docs"}, result.LoadedServers)
			_, err := turn().MCP.ListProjectTools("/proj")
			require.NoError(t, err)
			assert.Equal(t, 2, router.sent("mcp.ensure_loaded"))
		})
	}
}
