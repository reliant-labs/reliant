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
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ownerProbingRouter is two connected machines: it answers the Resolver's
// liveness checks and stats from which machine holds which directory, and
// records the machine every other command went to.
type ownerProbingRouter struct {
	mu   sync.Mutex
	dirs map[string]string // path -> the machine that has it
	sent []string          // "<machine> <command>", stats excluded
}

func (r *ownerProbingRouter) IsDaemonOnline(context.Context, string, *toolexec.DaemonSelector) (bool, error) {
	return true, nil
}

func (r *ownerProbingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if commandType == "fs.stat" {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(payload, &req)
		present := r.dirs[req.Path] == daemonID
		return json.Marshal(map[string]bool{"exists": present, "is_dir": present})
	}
	r.sent = append(r.sent, daemonID+" "+commandType)
	return json.Marshal(map[string]any{"nodes": []any{}})
}

func (r *ownerProbingRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, "default", commandType, payload, timeoutMs)
}

func (r *ownerProbingRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return "default", nil
}

// The Files tab scoped to a chat alone (no worktree_id) lists the chat's
// branch workspace, so it reads it on the machine that holds it, never on the
// machine a stale chat pin names. The workspace here predates owners being
// recorded: the first request settles it from disk and records it.
func TestFileSystemProxy_ChatOnlyScopeReadsTheChatsWorkspaceOnItsMachine(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)
	ctx := fsProxyContext()
	now := time.Now().UTC()
	machineA, machineB := "a-"+uuid.NewString(), "b-"+uuid.NewString()
	for _, id := range []string{machineA, machineB} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: fsProxyTestUser}))
		require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, id, "/p", nil))
	}
	worktreeID := uuid.NewString()
	wtPath := "/home/workspace/.reliant/worktrees/" + worktreeID
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{ID: worktreeID, Name: "feature", Path: wtPath,
		Branch: "feature", BaseBranch: "main", ProjectID: projectID, Status: 1,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: fsProxyTestUser,
		WorktreeID: &worktreeID, ActiveDaemonID: &machineB, // a pin from before StartChat refused it
		CreatedAt: now, UpdatedAt: now, LastActive: now}))

	router := &ownerProbingRouter{dirs: map[string]string{wtPath: machineA}}
	svc := newProxy(t, router, repo)
	_, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, ChatId: &chatID, Path: "/"}))
	require.NoError(t, err)

	assert.Equal(t, []string{machineA + " fs.get_tree"}, router.sent, "the workspace's machine, not the chat's stale pin")
	wt, err := repo.GetWorktree(ctx, worktreeID)
	require.NoError(t, err)
	require.NotNil(t, wt.DaemonID, "settled on first use")
	assert.Equal(t, machineA, *wt.DaemonID)
}
