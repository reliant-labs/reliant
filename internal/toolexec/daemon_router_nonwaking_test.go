// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// suspendedRegistry reports one daemon as suspended and counts resumes.
type suspendedRegistry struct {
	fakeRegistryClient
	resumes atomic.Int32
	bearer  atomic.Value
}

func (f *suspendedRegistry) ResolveDaemon(context.Context, *connect.Request[reliantv1.ResolveDaemonRequest]) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Found:  true,
		Daemon: &reliantv1.DaemonInfo{DaemonId: "daemon-suspended", Status: reliantv1.DaemonStatus_DAEMON_STATUS_IDLE},
	}), nil
}

func (f *suspendedRegistry) ResumeDaemon(_ context.Context, req *connect.Request[reliantv1.ResumeDaemonRequest]) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	f.resumes.Add(1)
	f.bearer.Store(req.Header().Get("Authorization"))
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
}

func newSuspendedRouter(t *testing.T) (*NATSDaemonRouter, *suspendedRegistry) {
	t.Helper()
	reg := &suspendedRegistry{}
	return NewNATSDaemonRouter(nil, WithControlPlaneClient(reg)), reg
}

var pinned = &DaemonSelector{ID: "daemon-suspended"}

func TestToolRequestAgainstSuspendedPinnedDaemonIsPendingAndNeverResumes(t *testing.T) {
	router, reg := newSuspendedRouter(t)
	_, err := router.SendToolRequestSyncWithSelector(context.Background(), "u", &ToolExecutionRequest{RequestID: "r", ToolName: "bash"}, pinned)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Zero(t, reg.resumes.Load(), "a tool call must never resume a daemon")
}

func TestMCPCommandWithSelectorAgainstSuspendedDaemonIsPendingAndNeverResumes(t *testing.T) {
	router, reg := newSuspendedRouter(t)
	_, err := SendDaemonCommandForSelector(context.Background(), router, "u", pinned, "mcp.call", []byte(`{}`), 1000)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Zero(t, reg.resumes.Load())
}

func TestDaemonCommandWithNilSelectorAgainstSuspendedDaemonIsPendingAndNeverResumes(t *testing.T) {
	router, reg := newSuspendedRouter(t)
	_, err := router.SendDaemonCommand(context.Background(), "u", "mcp.call", []byte(`{}`), 1000)
	require.Error(t, err)
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	_, err = router.SendToolRequestSync(context.Background(), "u", &ToolExecutionRequest{RequestID: "r", ToolName: "bash"})
	assert.True(t, IsDaemonPending(err), "got: %v", err)
	assert.Zero(t, reg.resumes.Load())
}

func TestEnsureAwakeResumesTheSuspendedDaemonOnceWithTheUsersJWT(t *testing.T) {
	const userID = "user-ensure-awake"
	auth.SetUserJWT(userID, "jwt-attended")
	t.Cleanup(func() { auth.SetUserJWT(userID, "") })
	router, reg := newSuspendedRouter(t)

	id, err := router.EnsureAwake(context.Background(), userID, pinned)
	require.NoError(t, err)
	assert.Equal(t, "daemon-suspended", id)
	assert.EqualValues(t, 1, reg.resumes.Load())
	assert.Equal(t, "Bearer jwt-attended", reg.bearer.Load())
}

// The structural guard: tool-time code holds a DaemonRouter, which must have
// no way to wake. If someone adds a wake method to the interface, this fails.
func TestToolTimeInterfacesHaveNoWakeMethod(t *testing.T) {
	for _, iface := range []reflect.Type{
		reflect.TypeOf((*DaemonRouter)(nil)).Elem(),
		reflect.TypeOf((*selectorCommandSender)(nil)).Elem(),
	} {
		for i := 0; i < iface.NumMethod(); i++ {
			name := iface.Method(i).Name
			assert.NotContains(t, name, "Wake", "%s.%s", iface.Name(), name)
			assert.NotContains(t, name, "Resume", "%s.%s", iface.Name(), name)
			assert.NotEqual(t, "EnsureAwake", name, "%s", iface.Name())
		}
	}
	assert.NotNil(t, reflect.TypeOf((*DaemonWaker)(nil)).Elem().MethodByName)
}
