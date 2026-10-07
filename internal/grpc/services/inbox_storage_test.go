// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

type neverSender struct{ called bool }

func (n *neverSender) SendDaemonCommandToDaemon(context.Context, string, string, string, []byte, int32) ([]byte, error) {
	n.called = true
	return nil, assert.AnError
}

// storageFixture is a user with one daemon, one archived worktree the daemon
// holds as dirty, and a low-disk report.
func storageFixture(t *testing.T) (*inboxFixture, string, string) {
	t.Helper()
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: strPtr("MacBook")}))
	require.NoError(t, f.repo.UpsertDaemonAttachment(f.ctx, &db.DaemonAttachment{DaemonID: daemonID, UserID: f.userID, Source: db.DaemonAttachmentSourceInbound}))

	now := time.Now().UTC()
	wtID := "wt-" + uuid.NewString()
	require.NoError(t, f.repo.CreateWorktree(f.ctx, &db.Worktree{
		ID: wtID, Name: "fix-login", Path: "/home/u/.reliant/worktrees/proj/fix-login", Branch: "feat/x", BaseBranch: "main",
		ProjectID: f.project(), DaemonID: &daemonID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, f.repo.ArchiveWorktree(f.ctx, wtID))
	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, wtID, &db.CleanupMetadata{HeldReason: "dirty", HeldDetail: "2 changed", SizeBytes: 5_000_000}))
	state, _ := json.Marshal(worktreesweep.StorageState{Path: "/home/u/.reliant/worktrees", FreeBytes: 20 << 30, TotalBytes: 500 << 30, ReportedAt: now})
	require.NoError(t, f.repo.SetDaemonStorageState(f.ctx, daemonID, string(state)))
	return f, daemonID, wtID
}

func TestListInbox_StorageItemShowsDiskAndHeldWorktrees(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	resp := f.list(t, nil)
	items := inboxByKind(resp.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, items, 1)
	s := items[0].GetStorage()
	require.NotNil(t, s)
	assert.Equal(t, daemonID, s.DaemonId)
	assert.Equal(t, "MacBook", s.DaemonName)
	assert.True(t, s.DiskLow)
	assert.True(t, s.Online)
	assert.EqualValues(t, 500<<30, s.DiskTotalBytes)
	require.Len(t, s.Held, 1)
	assert.Equal(t, wtID, s.Held[0].WorktreeId)
	assert.Equal(t, "dirty", s.Held[0].Reason)
	assert.EqualValues(t, 5_000_000, s.Held[0].SizeBytes)
	assert.Contains(t, items[0].ItemId, "storage:"+daemonID+":")
	assert.EqualValues(t, 0, resp.BlockingCount, "storage never blocks a run")
	assert.True(t, resp.HasInformational)
}

func TestListInbox_StorageItemAppearsInEveryProjectScope(t *testing.T) {
	f, _, _ := storageFixture(t)
	resp, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{ProjectId: strPtr("some-other-project")}))
	require.NoError(t, err)
	assert.Len(t, inboxByKind(resp.Msg.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE), 1)
}

func TestListInbox_NoStorageItemWhenDiskIsFineAndNothingHeld(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))
	state, _ := json.Marshal(worktreesweep.StorageState{FreeBytes: 800 << 30, TotalBytes: 1000 << 30, ReportedAt: time.Now()})
	require.NoError(t, f.repo.SetDaemonStorageState(f.ctx, daemonID, string(state)))
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE))
}

func TestListInbox_StorageItemIsScopedToItsOwner(t *testing.T) {
	_, _, _ = storageFixture(t)
	other := newInboxFixture(t)
	assert.Empty(t, inboxByKind(other.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE))
}

func TestStorageItem_CanBeDismissedAndReturnsWhenThePictureChanges(t *testing.T) {
	f, daemonID, _ := storageFixture(t)
	first := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)[0]
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: []string{first.ItemId}}))
	require.NoError(t, err)
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE))

	now := time.Now().UTC()
	id2 := "wt-" + uuid.NewString()
	require.NoError(t, f.repo.CreateWorktree(f.ctx, &db.Worktree{ID: id2, Name: "second", Path: "/x/second", Branch: "b", BaseBranch: "main",
		ProjectID: f.project(), DaemonID: &daemonID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, f.repo.ArchiveWorktree(f.ctx, id2))
	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, id2, &db.CleanupMetadata{HeldReason: "unpushed"}))
	again := inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, again, 1, "a new held worktree is a new problem and must not stay dismissed")
	assert.NotEqual(t, first.ItemId, again[0].ItemId)
}

func TestCleanupStorage_RejectsAnotherUsersMachineAndAskedNothing(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	sender := &neverSender{}
	f.inbox.WithSweeper(worktreesweep.New(f.repo, sender))

	otherCtx, _, _ := f.otherUser(t)
	_, err := f.inbox.CleanupStorage(otherCtx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.False(t, sender.called, "another user's machine must never be contacted")

	_, err = f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCleanupStorage_OfflineMachineIsAPreconditionNotAnError(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	_, err := f.repo.DB.ExecContext(f.ctx, `DELETE FROM daemon_attachment WHERE daemon_id = $1`, daemonID)
	require.NoError(t, err)
	f.inbox.WithSweeper(worktreesweep.New(f.repo, &neverSender{}))
	_, err = f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestCleanupStorage_UnavailableWithoutASweeper(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	_, err := f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// slowSender answers only after the 10s default RPC deadline would have fired.
type slowSender struct {
	done chan struct{}
}

func (s *slowSender) SendDaemonCommandToDaemon(ctx context.Context, _, _, _ string, _ []byte, _ int32) ([]byte, error) {
	defer close(s.done)
	time.Sleep(300 * time.Millisecond)
	return nil, assert.AnError
}

// S6: Clean up goes through the REAL timeout interceptor. It must return
// accepted at once, and the work must outlive the request's context.
func TestCleanupStorage_ReturnsAtOnceAndSurvivesTheRequestContext(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	sender := &slowSender{done: make(chan struct{})}
	f.inbox.WithSweeper(worktreesweep.New(f.repo, sender))

	ic := interceptors.NewTimeoutInterceptor().Interceptor()
	var deadline time.Duration
	call := ic(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if d, ok := ctx.Deadline(); ok {
			deadline = time.Until(d)
		}
		return f.inbox.CleanupStorage(ctx, req.(*connect.Request[reliantv1.CleanupStorageRequest]))
	})
	req := connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}})
	start := time.Now()
	resp, err := call(f.ctx, req)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 250*time.Millisecond, "the RPC must not wait for the machine")
	_ = deadline

	out := resp.(*connect.Response[reliantv1.CleanupStorageResponse]).Msg
	require.Len(t, out.Results, 1)
	assert.Equal(t, reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_ACCEPTED, out.Results[0].Outcome)

	select {
	case <-sender.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the background clean-up never ran")
	}
	require.Eventually(t, func() bool {
		wt, err := f.repo.GetWorktree(f.ctx, wtID)
		return err == nil && wt.CleanupMetadata != nil && !wt.CleanupMetadata.Cleaning
	}, 5*time.Second, 50*time.Millisecond, "a failed clean-up clears its in-progress flag")
}

func TestCleanupStorage_NotRemovableWorktreesAreSkippedNotAccepted(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, wtID, &db.CleanupMetadata{HeldReason: "data"}))
	sender := &neverSender{}
	f.inbox.WithSweeper(worktreesweep.New(f.repo, sender))
	resp, err := f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Results, 1)
	assert.Equal(t, reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_SKIPPED, resp.Msg.Results[0].Outcome)
	assert.False(t, sender.called)
}

// NIT: the nav-badge poll reuses the storage view for a minute.
func TestListInbox_StorageViewIsCachedBetweenPolls(t *testing.T) {
	f, _, _ := storageFixture(t)
	first := inboxByKind(f.inbox.mustList(t, f), reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, first, 1)
	_, err := f.repo.DB.ExecContext(f.ctx, `UPDATE worktrees SET cleanup_metadata = NULL`)
	require.NoError(t, err)
	cached := inboxByKind(f.inbox.mustList(t, f), reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, cached, 1)
	assert.Len(t, cached[0].GetStorage().Held, 1, "served from the cache")
	f.inbox.storageCache.Delete(f.userID)
	fresh := inboxByKind(f.inbox.mustList(t, f), reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
	require.Len(t, fresh, 1, "the disk is still critically low")
	assert.Empty(t, fresh[0].GetStorage().Held, "the held worktree is gone once the cache is dropped")
}

func (s *InboxService) mustList(t *testing.T, f *inboxFixture) []*reliantv1.InboxItem {
	t.Helper()
	resp, err := s.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	return resp.Msg.Items
}
