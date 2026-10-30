// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A workspace made from the new-chat composer goes on the machine its "Runs
// on" names, so the chat that follows runs where its checkout is.

func (f *placementFixture) ownedDaemon(t *testing.T, userID string) string {
	t.Helper()
	id := "daemon-" + uuid.NewString()[:8]
	require.NoError(t, f.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: id, UserID: userID}))
	return id
}

func TestCreateWorktree_PlacesOnTheRequestedMachine(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{DaemonId: &machine})
	require.NoError(t, err)

	assertPlacedOn(t, f.router, wt, machine)
}

func TestCreateWorktree_RefusesARequestedMachineThatIsNotTheCallers(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	foreign := f.ownedDaemon(t, uuid.NewString())

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{DaemonId: &foreign})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"), "nothing may be created on another user's machine")
}

// A copy of a workspace is made where the workspace is; naming another
// machine contradicts that.
func TestCreateWorktree_RefusesARequestedMachineOtherThanTheSources(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)
	source := f.worktree(t, "daemon-b")

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{SourceWorktreeId: &source.ID, DaemonId: &machine})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"))
}

func TestCreateWorktree_RefusesARequestedMachineOtherThanTheChats(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)
	chatID := f.chat(t, f.userID, "daemon-b", "")

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &chatID, DaemonId: &machine})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// The requested-machine path keeps the placement path's project binding: a
// chat of another project of the caller's cannot own this project's workspace.
func TestCreateWorktree_RefusesARequestedMachineWithAnotherProjectsChat(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machine := f.ownedDaemon(t, f.userID)
	now := time.Now().UTC()
	otherProject := uuid.NewString()
	require.NoError(t, f.repo.CreateProject(context.Background(), &db.Project{
		ID: otherProject, UserID: f.userID, Name: "Other", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	c := &db.Chat{ID: uuid.NewString(), UserID: f.userID, Title: "elsewhere", ProjectID: otherProject, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, f.repo.CreateChat(context.Background(), c))

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &c.ID, DaemonId: &machine})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"))
}

// probingRouter is a placementRouter whose machines are all online and
// answer fs.stat: only holder has the directories in held.
type probingRouter struct {
	placementRouter
	holder string
	held   map[string]bool
}

func (r *probingRouter) IsDaemonOnline(context.Context, string, *toolexec.DaemonSelector) (bool, error) {
	return true, nil
}

func (r *probingRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "fs.stat" {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(payload, &req)
		present := daemonID == r.holder && r.held[req.Path]
		return json.Marshal(map[string]bool{"exists": present, "is_dir": present})
	}
	return r.placementRouter.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

// A source with no recorded owner is settled before the requested machine is
// judged: found on B, a copy requested on A is refused, never made where the
// source's files are not.
func TestCreateWorktree_RefusesARequestedMachineOtherThanAnUnattributedSourcesHolder(t *testing.T) {
	t.Parallel()

	f := newPlacementFixture(t)
	machineA := f.ownedDaemon(t, f.userID)
	machineB := f.ownedDaemon(t, f.userID)
	source := f.worktree(t, "") // unattributed: no daemon_id
	router := &probingRouter{holder: machineB, held: map[string]bool{source.Path: true}}
	f.svc = newWorktreeService(t, f.repo, router)

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{SourceWorktreeId: &source.ID, DaemonId: &machineA})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Empty(t, router.daemonsFor("worktree.create"), "no copy may be made away from the source's files")
	settled, err := f.repo.GetWorktree(context.Background(), source.ID)
	require.NoError(t, err)
	require.NotNil(t, settled.DaemonID)
	assert.Equal(t, machineB, *settled.DaemonID, "the source's holder is recorded")
}

// The row carries its machine to the web, so the composer can tell which
// workspaces a machine holds.
func TestWorktreeProto_CarriesItsMachine(t *testing.T) {
	t.Parallel()

	owner := "daemon-b"
	assert.Equal(t, owner, worktreeToProto(&db.Worktree{ID: "w", DaemonID: &owner}).GetDaemonId())
	assert.Nil(t, worktreeToProto(&db.Worktree{ID: "main", IsMain: true}).DaemonId)
}
