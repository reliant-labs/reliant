// Copyright (c) 2025 Reliant Labs
package services

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

// fsRoutingDefaultDaemon is what default resolution reaches in these tests:
// the user's default machine, which is NOT the chat's machine.
const fsRoutingDefaultDaemon = "daemon-default"

// fsRoutingRouter records the machine every filesystem command was delivered
// to. A command left to default resolution is recorded as
// fsRoutingDefaultDaemon, so it is distinguishable from one sent to a chat's
// machine.
type fsRoutingRouter struct {
	worktreeTestDaemonRouter

	mu      sync.Mutex
	targets map[string][]string
}

func (r *fsRoutingRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return fsRoutingDefaultDaemon, nil
}

func (r *fsRoutingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	return r.deliver(fsRoutingDefaultDaemon, commandType)
}

func (r *fsRoutingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	return r.deliver(daemonID, commandType)
}

func (r *fsRoutingRouter) deliver(daemonID, commandType string) ([]byte, error) {
	r.mu.Lock()
	if r.targets == nil {
		r.targets = make(map[string][]string)
	}
	r.targets[commandType] = append(r.targets[commandType], daemonID)
	r.mu.Unlock()

	switch commandType {
	case "fs.get_tree":
		return json.Marshal(map[string]any{"nodes": []any{}})
	case "fs.read_file":
		return json.Marshal(map[string]any{"content": "hello"})
	case "fs.preview_info":
		return json.Marshal(map[string]any{"name": "a.txt", "path": "a.txt", "viewer_kind": "text"})
	case "fs.stat":
		return json.Marshal(map[string]any{"exists": true})
	}
	return json.Marshal(map[string]any{})
}

// daemonsFor returns the machines commandType was delivered to, in send order.
func (r *fsRoutingRouter) daemonsFor(commandType string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets[commandType]...)
}

// sent reports whether any command reached any machine.
func (r *fsRoutingRouter) sent() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.targets) > 0
}

type fsRoutingFixture struct {
	*placementFixture
	router *fsRoutingRouter
	fs     *FileSystemProxyService
}

func newFSRoutingFixture(t *testing.T) *fsRoutingFixture {
	t.Helper()
	pf := newPlacementFixture(t)
	router := &fsRoutingRouter{}
	return &fsRoutingFixture{
		placementFixture: pf,
		router:           router,
		fs:               NewFileSystemProxyService(router, pf.repo),
	}
}

// browse opens the Files tab and a file preview the way the web app does for
// a chat: the tree, the preview classification, and the content.
func (f *fsRoutingFixture) browse(t *testing.T, worktreeID, chatID *string) error {
	t.Helper()
	if _, err := f.fs.GetFileTree(f.ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{
		ProjectId: f.projectID, Path: "/", WorktreeId: worktreeID, ChatId: chatID,
	})); err != nil {
		return err
	}
	if _, err := f.fs.GetFilePreviewInfo(f.ctx, connect.NewRequest(&reliantv1.GetFilePreviewInfoRequest{
		ProjectId: f.projectID, Path: "a.txt", WorktreeId: worktreeID, ChatId: chatID,
	})); err != nil {
		return err
	}
	_, err := f.fs.GetFileContent(f.ctx, connect.NewRequest(&reliantv1.GetFileContentRequest{
		ProjectId: f.projectID, Path: "a.txt", WorktreeId: worktreeID, ChatId: chatID,
	}))
	return err
}

// assertBrowsedOn checks the tree, the preview and the read all reached
// daemonID and nothing else.
func (f *fsRoutingFixture) assertBrowsedOn(t *testing.T, daemonID string) {
	t.Helper()
	for _, cmd := range []string{"fs.get_tree", "fs.preview_info", "fs.read_file"} {
		got := f.router.daemonsFor(cmd)
		require.NotEmpty(t, got, "%s was never sent", cmd)
		for _, d := range got {
			assert.Equal(t, daemonID, d, "%s went to the wrong machine", cmd)
		}
	}
}

// TestFileSystemProxy_ChatOnMachineB_ReadsFromB is the regression for "the
// file tree and preview read from the user's default machine": a chat pinned
// to machine B, on the project's main checkout, opens its Files tab. Its tools
// run on B, so the tree and the preview must come from B. Before the fix every
// request went to the default machine.
func TestFileSystemProxy_ChatOnMachineB_ReadsFromB(t *testing.T) {
	f := newFSRoutingFixture(t)
	chatID := f.chat(t, f.userID, "daemon-b", "")

	require.NoError(t, f.browse(t, nil, &chatID))
	f.assertBrowsedOn(t, "daemon-b")
}

// The web app names the workspace it shows, and for the main checkout that is
// the main worktree row, which no machine owns. The chat still decides.
func TestFileSystemProxy_ChatOnMachineB_MainWorktreeRow_ReadsFromB(t *testing.T) {
	f := newFSRoutingFixture(t)
	main := f.worktree(t, "")
	chatID := f.chat(t, f.userID, "daemon-b", "")

	require.NoError(t, f.browse(t, &main.ID, &chatID))
	f.assertBrowsedOn(t, "daemon-b")
}

// A chat with no pin, bound to a worktree machine B owns, runs its tools on B.
func TestFileSystemProxy_ChatInOwnedWorktree_ReadsFromOwner(t *testing.T) {
	f := newFSRoutingFixture(t)
	wt := f.worktree(t, "daemon-b")
	chatID := f.chat(t, f.userID, "", wt.ID)

	require.NoError(t, f.browse(t, &wt.ID, &chatID))
	f.assertBrowsedOn(t, "daemon-b")
}

// The chat's pin beats its worktree's owner, as it does for the chat's tools.
func TestFileSystemProxy_ChatPinBeatsItsWorktreeOwner(t *testing.T) {
	f := newFSRoutingFixture(t)
	wt := f.worktree(t, "daemon-b")
	chatID := f.chat(t, f.userID, "daemon-c", wt.ID)

	require.NoError(t, f.browse(t, &wt.ID, &chatID))
	f.assertBrowsedOn(t, "daemon-c")
}

// Without a chat, an owned worktree is read from its owner: no other machine
// has the checkout.
func TestFileSystemProxy_OwnedWorktreeWithoutChat_ReadsFromOwner(t *testing.T) {
	f := newFSRoutingFixture(t)
	wt := f.worktree(t, "daemon-b")

	require.NoError(t, f.browse(t, &wt.ID, nil))
	f.assertBrowsedOn(t, "daemon-b")
}

// A request that names an owned worktree other than the chat's own is about
// that worktree, so it goes to the worktree's owner, not the chat's machine.
func TestFileSystemProxy_OtherOwnedWorktree_ReadsFromItsOwner(t *testing.T) {
	f := newFSRoutingFixture(t)
	other := f.worktree(t, "daemon-b")
	chatID := f.chat(t, f.userID, "daemon-c", "")

	require.NoError(t, f.browse(t, &other.ID, &chatID))
	f.assertBrowsedOn(t, "daemon-b")
}

// Nothing names a machine: default resolution, as before.
func TestFileSystemProxy_NoMachineNamed_UsesDefault(t *testing.T) {
	f := newFSRoutingFixture(t)
	main := f.worktree(t, "")
	chatID := f.chat(t, f.userID, "", "")

	require.NoError(t, f.browse(t, &main.ID, &chatID))
	f.assertBrowsedOn(t, fsRoutingDefaultDaemon)
}

// Writes follow the same rule as reads: a save must land on the disk the tree
// was read from.
func TestFileSystemProxy_ChatOnMachineB_WritesToB(t *testing.T) {
	f := newFSRoutingFixture(t)
	chatID := f.chat(t, f.userID, "daemon-b", "")

	_, err := f.fs.SaveFileContent(f.ctx, connect.NewRequest(&reliantv1.SaveFileContentRequest{
		ProjectId: f.projectID, Path: "a.txt", Content: "x", ChatId: &chatID,
	}))
	require.NoError(t, err)
	_, err = f.fs.SearchFiles(f.ctx, connect.NewRequest(&reliantv1.SearchFilesRequest{
		ProjectId: f.projectID, Query: "x", ChatId: &chatID,
	}))
	require.NoError(t, err)

	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("fs.write_file"))
	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("fs.search"))
}

// A chat that names a machine is a routing input now, so another user's chat
// must not steer a request. It is refused before anything is sent.
func TestFileSystemProxy_RefusesAnotherUsersChat(t *testing.T) {
	f := newFSRoutingFixture(t)
	chatID := f.chat(t, uuid.NewString(), "daemon-b", "")

	err := f.browse(t, nil, &chatID)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.False(t, f.router.sent(), "nothing may reach a daemon for another user's chat")
}

// A chat the UI still names after deleting it degrades to the routing a
// request without a chat gets, rather than failing the tree.
func TestFileSystemProxy_DeletedChat_FallsBackToDefault(t *testing.T) {
	f := newFSRoutingFixture(t)
	gone := uuid.NewString()

	require.NoError(t, f.browse(t, nil, &gone))
	f.assertBrowsedOn(t, fsRoutingDefaultDaemon)
}

// A chat from another project is stale UI context (a project switch racing
// the tree): it neither routes the request nor scopes it to its worktree.
func TestFileSystemProxy_ChatFromAnotherProject_IsIgnored(t *testing.T) {
	f := newFSRoutingFixture(t)
	now := time.Now().UTC()
	otherProject := uuid.NewString()
	require.NoError(t, f.repo.CreateProject(context.Background(), &db.Project{
		ID: otherProject, UserID: f.userID, Name: "Other", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	wt := f.worktree(t, "daemon-b")
	pinned := "daemon-b"
	chatID := uuid.NewString()
	require.NoError(t, f.repo.CreateChat(context.Background(), &db.Chat{
		ID: chatID, UserID: f.userID, Title: "Other project chat", ProjectID: otherProject,
		ActiveDaemonID: &pinned, WorktreeID: &wt.ID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	resp, err := f.fs.GetFileTree(f.ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{
		ProjectId: f.projectID, Path: "/", ChatId: &chatID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, []string{fsRoutingDefaultDaemon}, f.router.daemonsFor("fs.get_tree"))

	project, err := f.repo.GetProject(context.Background(), f.projectID)
	require.NoError(t, err)
	base, err := f.fs.requireProjectBase(f.ctx, f.projectID, nil, &chatID)
	require.NoError(t, err)
	assert.Equal(t, project.Path, base, "another project's chat must not scope this project's tree to its worktree")
}
