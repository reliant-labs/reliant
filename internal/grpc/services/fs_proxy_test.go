// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingFSDaemonRouter records the last daemon command it received so tests
// can assert that CreateDirectory routes an fs.mkdir to the daemon. It embeds
// worktreeTestDaemonRouter to satisfy the full DaemonRouter interface.
type recordingFSDaemonRouter struct {
	worktreeTestDaemonRouter
	lastCommandType string
	lastPayload     []byte
}

func (r *recordingFSDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.lastCommandType = commandType
	r.lastPayload = payload
	return json.Marshal(struct{}{})
}

func TestFileSystemProxy_CreateDirectory_RoutesMkdirToDaemon(t *testing.T) {
	router := &recordingFSDaemonRouter{}
	svc := NewFileSystemProxyService(router, nil)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	resp, err := svc.CreateDirectory(ctx, connect.NewRequest(&reliantv1.CreateDirectoryRequest{
		Path: "/home/workspace/projects/new-folder",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "/home/workspace/projects/new-folder", resp.Msg.Path)

	assert.Equal(t, "fs.mkdir", router.lastCommandType)
	var sent map[string]string
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "/home/workspace/projects/new-folder", sent["path"])
}

func TestFileSystemProxy_CreateDirectory_RejectsEmptyAndRelativePaths(t *testing.T) {
	router := &recordingFSDaemonRouter{}
	svc := NewFileSystemProxyService(router, nil)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	for _, path := range []string{"", "relative/path"} {
		_, err := svc.CreateDirectory(ctx, connect.NewRequest(&reliantv1.CreateDirectoryRequest{Path: path}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
	// No command should have been forwarded to the daemon.
	assert.Empty(t, router.lastCommandType)
}

func TestFileSystemProxy_CreateDirectory_RequiresAuth(t *testing.T) {
	router := &recordingFSDaemonRouter{}
	svc := NewFileSystemProxyService(router, nil)

	_, err := svc.CreateDirectory(context.Background(), connect.NewRequest(&reliantv1.CreateDirectoryRequest{
		Path: "/home/workspace/projects/new-folder",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
