package file_system

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// machineRecordingRouter records the machine every command went to.
type machineRecordingRouter struct {
	mu   sync.Mutex
	sent []string // "<machine> <command>"
}

func (r *machineRecordingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, daemonID+" "+commandType)
	return json.Marshal(map[string]any{"nodes": []any{}})
}

func (r *machineRecordingRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, "default", commandType, payload, timeoutMs)
}

func (r *machineRecordingRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return "default", nil
}

// The Files tab scoped to a chat alone (no worktree_id) lists the chat's
// branch workspace, so it reads it on the machine that holds it, never on the
// machine a stale chat pin names.
func TestFileSystemProxy_ChatOnlyScopeReadsTheChatsWorkspaceOnItsMachine(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)
	ctx := fsProxyContext()
	now := time.Now().UTC()
	machineA, machineB := "a-"+uuid.NewString(), "b-"+uuid.NewString()
	worktreeID := uuid.NewString()
	wtPath := "/home/workspace/.reliant/worktrees/" + worktreeID
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{ID: worktreeID, Name: "feature", Path: wtPath,
		Branch: "feature", BaseBranch: "main", ProjectID: projectID, DaemonID: &machineA, Status: 1,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: fsProxyTestUser,
		WorktreeID: &worktreeID, ActiveDaemonID: &machineB, // a pin from before StartChat refused it
		CreatedAt: now, UpdatedAt: now, LastActive: now}))

	router := &machineRecordingRouter{}
	svc := newProxy(t, router, repo)
	_, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, ChatId: &chatID, Path: "/"}))
	require.NoError(t, err)

	assert.Equal(t, []string{machineA + " fs.get_tree"}, router.sent, "the workspace's machine, not the chat's stale pin")
}

// shiftingDefaultRouter resolves default resolution to one machine when asked
// by id and delivers an unpinned command to another: a machine connected in
// between. Every pinned command is recorded with its machine.
type shiftingDefaultRouter struct {
	machineRecordingRouter
	resolves, delivers string
}

func (r *shiftingDefaultRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, r.delivers, commandType, payload, timeoutMs)
}

func (r *shiftingDefaultRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return r.resolves, nil
}

// A request left to default resolution is resolved on one machine and must
// run on THAT machine: its path is that machine's. Sent unpinned, a second
// resolution could land it — a delete included — on a machine that connected
// in between, with the first machine's path.
func TestFileSystemProxy_DefaultResolutionIsPinnedToTheMachineThePathCameFrom(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)
	ctx := fsProxyContext()
	resolved, other := "resolved-"+uuid.NewString(), "other-"+uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: resolved, UserID: fsProxyTestUser}))
	require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, resolved, "/srv/resolved/p", nil))

	router := &shiftingDefaultRouter{resolves: resolved, delivers: other}
	svc := newProxy(t, router, repo)
	_, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, Path: "/"}))
	require.NoError(t, err)

	assert.Equal(t, []string{resolved + " fs.get_tree"}, router.sent, "pinned to the machine whose path the request carries")
}
