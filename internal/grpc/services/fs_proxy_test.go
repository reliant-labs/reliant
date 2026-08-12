// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
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

func (r *recordingFSDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
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

// fsTreeStubRouter returns a canned fs.get_tree response and records the payload
// the proxy forwarded, so tests can assert depth threading and node conversion.
type fsTreeStubRouter struct {
	worktreeTestDaemonRouter
	lastPayload []byte
	respJSON    []byte
}

func (r *fsTreeStubRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *fsTreeStubRouter) SendDaemonCommand(_ context.Context, _ string, _ string, payload []byte, _ int32) ([]byte, error) {
	r.lastPayload = payload
	return r.respJSON, nil
}

// GetFileTree must forward depth to the daemon, rebase the daemon's
// subdir-relative node paths onto the project-relative request path, and
// propagate size + has_children into the proto response.
func TestFileSystemProxy_GetFileTree_PrefixesPathsAndForwardsDepth(t *testing.T) {
	daemonTree := `{"nodes":[
		{"name":"inner.txt","path":"inner.txt","type":"file","size":11},
		{"name":"sub","path":"sub","type":"directory","has_children":true}
	]}`
	router := &fsTreeStubRouter{respJSON: []byte(daemonTree)}
	svc := NewFileSystemProxyService(router, nil)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	// ProjectId empty → no DB lookup; resolvedPath == request path "pkg".
	resp, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{
		Path:  "pkg",
		Depth: 1,
	}))
	require.NoError(t, err)

	var sent map[string]any
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.EqualValues(t, 1, sent["depth"], "depth must be forwarded to the daemon")
	assert.Equal(t, "pkg", sent["path"])

	byName := map[string]*reliantv1.FileNode{}
	for _, f := range resp.Msg.Files {
		byName[f.Name] = f
	}

	inner := byName["inner.txt"]
	require.NotNil(t, inner)
	assert.Equal(t, "pkg/inner.txt", inner.Path, "file path must be rebased onto request path")
	assert.EqualValues(t, 11, inner.GetSize())

	sub := byName["sub"]
	require.NotNil(t, sub)
	assert.Equal(t, "pkg/sub", sub.Path, "dir path must be rebased onto request path")
	assert.Equal(t, reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY, sub.Type)
	assert.True(t, sub.GetHasChildren(), "has_children hint must propagate for lazy expansion")
}

// Root requests carry paths already relative to the project root, so the proxy
// must not prefix them.
func TestFileSystemProxy_GetFileTree_RootNotPrefixed(t *testing.T) {
	daemonTree := `{"nodes":[{"name":"pkg","path":"pkg","type":"directory","has_children":true}]}`
	router := &fsTreeStubRouter{respJSON: []byte(daemonTree)}
	svc := NewFileSystemProxyService(router, nil)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	resp, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{
		Path:  "/",
		Depth: 2,
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Files, 1)
	assert.Equal(t, "pkg", resp.Msg.Files[0].Path, "root-level paths must be left unchanged")
}
