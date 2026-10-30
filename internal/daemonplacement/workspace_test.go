// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type fakeWorkspaces struct {
	worktrees map[string]*db.Worktree
	chats     map[string]*db.Chat
	daemons   map[string]*db.Daemon
	// projects maps a project to its owner; unlisted projects are u1's.
	projects map[string]string
}

func (f fakeWorkspaces) GetProject(_ context.Context, id string) (*db.Project, error) {
	owner := "u1"
	if o, ok := f.projects[id]; ok {
		owner = o
	}
	return &db.Project{ID: id, UserID: owner}, nil
}

func (f fakeWorkspaces) GetWorktree(_ context.Context, id string) (*db.Worktree, error) {
	if wt, ok := f.worktrees[id]; ok {
		return wt, nil
	}
	return nil, fmt.Errorf("%w: %s", core.ErrWorktreeNotFound, id)
}

func (f fakeWorkspaces) GetChat(_ context.Context, id string) (*db.Chat, error) {
	if c, ok := f.chats[id]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
}

func (f fakeWorkspaces) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := f.daemons[id]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

func strp(s string) *string { return &s }

func workspaces() fakeWorkspaces {
	return fakeWorkspaces{
		worktrees: map[string]*db.Worktree{
			"main":   {ID: "main", ProjectID: "p1", IsMain: true},
			"branch": {ID: "branch", ProjectID: "p1", DaemonID: strp("daemon-b")},
			"theirs": {ID: "theirs", ProjectID: "p-theirs", DaemonID: strp("their-machine")},
		},
		projects: map[string]string{"p-theirs": "u2"},
		chats: map[string]*db.Chat{
			"chat-c":       {ID: "chat-c", UserID: "u1", ProjectID: "p1", ActiveDaemonID: strp("daemon-c"), WorktreeID: strp("main")},
			"chat-branch":  {ID: "chat-branch", UserID: "u1", ProjectID: "p1", ActiveDaemonID: strp("daemon-c"), WorktreeID: strp("branch")},
			"chat-nomach":  {ID: "chat-nomach", UserID: "u1", ProjectID: "p1", NoMachine: true, WorktreeID: strp("main")},
			"chat-other-p": {ID: "chat-other-p", UserID: "u1", ProjectID: "p2", ActiveDaemonID: strp("daemon-c")},
			"chat-foreign": {ID: "chat-foreign", UserID: "u2", ProjectID: "p1", ActiveDaemonID: strp("daemon-c")},
		},
	}
}

// Another user's worktree, or one of another project than the request
// names, is ErrForeignWorkspace before its machine is read: no caller can be
// handed another tenant's machine id.
func TestWorkspaceDaemon_ForeignWorkspaceIsNotRead(t *testing.T) {
	for _, projectID := range []string{"p-theirs", "p1", ""} {
		ws, err := WorkspaceDaemon(context.Background(), workspaces(), nil, "u1", projectID, "theirs", "")
		require.ErrorIs(t, err, ErrForeignWorkspace, projectID)
		assert.Empty(t, ws.DaemonID)
	}
	// One of the caller's own worktrees named under another project is refused too.
	_, err := WorkspaceDaemon(context.Background(), workspaces(), nil, "u1", "p2", "branch", "")
	require.ErrorIs(t, err, ErrForeignWorkspace)
}

func TestWorkspaceDaemon(t *testing.T) {
	cases := []struct {
		name               string
		worktreeID, chatID string
		want               Workspace
	}{
		{"owned worktree goes to its owner", "branch", "", Workspace{DaemonID: "daemon-b"}},
		{"owned worktree beats a mismatched chat pin", "branch", "chat-branch", Workspace{DaemonID: "daemon-b"}},
		{"main checkout goes to the chat's machine", "main", "chat-c", Workspace{DaemonID: "daemon-c"}},
		{"no worktree goes to the chat's machine", "", "chat-c", Workspace{DaemonID: "daemon-c"}},
		{"chat only: the chat's own branch workspace beats its mismatched pin", "", "chat-branch", Workspace{DaemonID: "daemon-b"}},
		{"main checkout without a chat is default resolution", "main", "", Workspace{}},
		{"a no-machine chat is marked", "main", "chat-nomach", Workspace{NoMachine: true}},
		{"a chat from another project is ignored", "main", "chat-other-p", Workspace{}},
		{"a deleted chat is ignored", "branch", "gone", Workspace{DaemonID: "daemon-b"}},
		{"a deleted worktree has no owner", "gone", "chat-c", Workspace{DaemonID: "daemon-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WorkspaceDaemon(context.Background(), workspaces(), nil, "u1", "p1", tc.worktreeID, tc.chatID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestWorkspaceDaemon_ForeignChat(t *testing.T) {
	_, err := WorkspaceDaemon(context.Background(), workspaces(), nil, "u1", "p1", "main", "chat-foreign")
	assert.ErrorIs(t, err, ErrForeignChat)
}

func TestWorkspaceMachineConflict(t *testing.T) {
	owned := &db.Worktree{DaemonID: strp("daemon-b")}
	main := &db.Worktree{IsMain: true}

	owner, conflict := WorkspaceMachineConflict(owned, "daemon-a")
	assert.True(t, conflict)
	assert.Equal(t, "daemon-b", owner)

	_, conflict = WorkspaceMachineConflict(owned, "daemon-b")
	assert.False(t, conflict, "its own machine")
	_, conflict = WorkspaceMachineConflict(owned, "")
	assert.False(t, conflict, "no machine named: placement picks the owner")
	_, conflict = WorkspaceMachineConflict(main, "daemon-a")
	assert.False(t, conflict, "the main checkout is on every machine")
	_, conflict = WorkspaceMachineConflict(nil, "daemon-a")
	assert.False(t, conflict)
}

func TestMachineName(t *testing.T) {
	managed := "managed"
	selfHosted := "self_hosted"
	r := fakeWorkspaces{daemons: map[string]*db.Daemon{
		"named":    {ID: "named", Name: "gpu-box"},
		"managed":  {ID: "managed", DaemonType: &managed, Hostname: strp("ws-ws-2aab1465")},
		"host":     {ID: "host", DaemonType: &selfHosted, Hostname: strp("Seans-MacBook-Pro.local")},
		"uuidhost": {ID: "uuidhost", DaemonType: &selfHosted, Hostname: strp("0b4f2a39-1111-2222-3333-444455556666")},
	}}
	ctx := context.Background()
	assert.Equal(t, `"gpu-box"`, MachineName(ctx, r, "named"))
	assert.Equal(t, "cloud machine managed", MachineName(ctx, r, "managed"), "a pod hostname is never a name")
	assert.Equal(t, `"Seans-MacBook-Pro.local"`, MachineName(ctx, r, "host"))
	assert.Equal(t, "self-hosted machine uuidhost", MachineName(ctx, r, "uuidhost"))
	assert.Equal(t, "machine 12345678", MachineName(ctx, r, "1234567890"))
}

// A request scoped to a chat alone (the Files tab with no worktree_id) reads
// the chat's branch workspace, so it goes to that workspace's machine, even
// one no row recorded yet: the Resolver settles it on first use. The chat's
// stale pin to another machine is never the route.
func TestWorkspaceDaemon_ChatOnlySettlesTheChatsWorkspace(t *testing.T) {
	f := newOwnerFixture()
	f.machine("a", true, wtPath) // holds the checkout
	f.machine("b", true)         // the chat's stale pin
	r := workspaces()
	r.worktrees["wt-1"] = unattributed()
	r.chats["chat-stale"] = &db.Chat{ID: "chat-stale", UserID: "u1", ProjectID: "p1", ActiveDaemonID: strp("b"), WorktreeID: strp("wt-1")}

	ws, err := WorkspaceDaemon(context.Background(), r, NewResolver(f, f), "u1", "p1", "", "chat-stale")
	require.NoError(t, err)
	assert.Equal(t, Workspace{DaemonID: "a"}, ws)
	assert.Equal(t, map[string]string{"wt-1": "a"}, f.adopted, "settled and recorded on first use")
}

// ChatDaemonID is where the chat's tools run: a branch workspace's owner
// beats a pin naming another machine (a session pin yields, as in
// toolDaemonSelector); the main checkout and no workspace keep the pin.
func TestChatDaemonID_TheWorkspaceOwnerBeatsAStalePin(t *testing.T) {
	r := workspaces()
	for chatID, want := range map[string]string{"chat-branch": "daemon-b", "chat-c": "daemon-c", "chat-other-p": "daemon-c"} {
		got, err := ChatDaemonID(context.Background(), r, r.chats[chatID])
		require.NoError(t, err)
		assert.Equal(t, want, got, chatID)
	}
}
