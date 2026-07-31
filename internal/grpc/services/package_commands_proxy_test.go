// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// pkgProxyRouter stubs the daemon round trip for PackageCommandsProxyService.
// It embeds worktreeTestDaemonRouter (defined in worktree_test.go) to satisfy
// the full DaemonRouter interface, and lets each test dictate the reply for
// pkg.list_commands.
type pkgProxyRouter struct {
	worktreeTestDaemonRouter
	lastCommandType string
	lastPayload     []byte
	reply           []byte
	replyErr        error
}

func (r *pkgProxyRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *pkgProxyRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.lastCommandType = commandType
	r.lastPayload = payload
	return r.reply, r.replyErr
}

func authedCtx() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
}

// A successful daemon reply is converted to proto and routed as pkg.list_commands.
func TestPackageCommandsProxy_ListCommands_Success(t *testing.T) {
	daemonResp := pkgmgr.CommandListResponse{
		Commands: map[pkgmgr.PackageType][]pkgmgr.Command{
			pkgmgr.PackageTypeTaskfile: {
				{Name: "build", Command: "task build", PackageType: pkgmgr.PackageTypeTaskfile},
			},
		},
		DetectedTypes: []pkgmgr.PackageType{pkgmgr.PackageTypeTaskfile},
	}
	replyBytes, err := json.Marshal(daemonResp)
	require.NoError(t, err)

	router := &pkgProxyRouter{reply: replyBytes}
	svc := NewPackageCommandsProxyService(router, nil)

	resp, err := svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		Path: strPtr("/daemon/workspace/proj"),
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Routed to the daemon command with the resolved working dir.
	assert.Equal(t, "pkg.list_commands", router.lastCommandType)
	var sent map[string]string
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "/daemon/workspace/proj", sent["working_dir"])

	// Converted to proto.
	assert.Equal(t, []string{"taskfile"}, resp.Msg.DetectedTypes)
	taskCommands := resp.Msg.Commands["taskfile"]
	require.NotNil(t, taskCommands)
	require.Len(t, taskCommands.Commands, 1)
	assert.Equal(t, "build", taskCommands.Commands[0].Name)
}

// A directory that exists on the daemon but has no manifests comes back as an
// empty (successful) response — not an error.
func TestPackageCommandsProxy_ListCommands_EmptyIsSuccess(t *testing.T) {
	replyBytes, err := json.Marshal(pkgmgr.CommandListResponse{
		Commands:      map[pkgmgr.PackageType][]pkgmgr.Command{},
		DetectedTypes: []pkgmgr.PackageType{},
	})
	require.NoError(t, err)

	router := &pkgProxyRouter{reply: replyBytes}
	svc := NewPackageCommandsProxyService(router, nil)

	resp, err := svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		Path: strPtr("/daemon/workspace/empty"),
	}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Commands)
	assert.Empty(t, resp.Msg.DetectedTypes)
}

// The core regression guard: a dispatch failure must surface as a LOUD Connect
// error, never as an empty command list.
func TestPackageCommandsProxy_ListCommands_DispatchFailure_IsLoud(t *testing.T) {
	router := &pkgProxyRouter{replyErr: fmt.Errorf("daemon command pkg.list_commands (subject x, timeout 30s): nats: timeout")}
	svc := NewPackageCommandsProxyService(router, nil)

	resp, err := svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		Path: strPtr("/daemon/workspace/proj"),
	}))
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// A missing working dir on the daemon maps to NotFound (via the sentinel), which
// is distinct from a retryable outage.
func TestPackageCommandsProxy_ListCommands_MissingDir_IsNotFound(t *testing.T) {
	router := &pkgProxyRouter{
		replyErr: fmt.Errorf(`daemon command "pkg.list_commands" failed: working dir does not exist: /daemon/workspace/gone`),
	}
	svc := NewPackageCommandsProxyService(router, nil)

	resp, err := svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		Path: strPtr("/daemon/workspace/gone"),
	}))
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// A malformed daemon reply is a loud Internal error, not a silent empty result.
func TestPackageCommandsProxy_ListCommands_MalformedReply_IsLoud(t *testing.T) {
	router := &pkgProxyRouter{reply: []byte("not json")}
	svc := NewPackageCommandsProxyService(router, nil)

	resp, err := svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		Path: strPtr("/daemon/workspace/proj"),
	}))
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// Neither worktree_id nor path is an InvalidArgument, and nothing is forwarded.
func TestPackageCommandsProxy_ListCommands_NoTargetIsInvalidArgument(t *testing.T) {
	router := &pkgProxyRouter{}
	svc := NewPackageCommandsProxyService(router, nil)

	_, err := svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Empty(t, router.lastCommandType, "no daemon command should be forwarded")
}

// Missing auth is Unauthenticated.
func TestPackageCommandsProxy_ListCommands_RequiresAuth(t *testing.T) {
	router := &pkgProxyRouter{}
	svc := NewPackageCommandsProxyService(router, nil)

	_, err := svc.ListCommands(context.Background(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		Path: strPtr("/daemon/workspace/proj"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}