// Copyright (c) 2026 Reliant Labs
package inbox

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// Worktrees marked to be reclaimed (worktreesweep mark.go): one Inbox item
// each, with Keep and Reclaim now, acting only on the mark shown.

// unmarkSender records the daemon commands it is sent and answers them as a
// daemon whose worktree has landed and still carries tombstone mark (until
// it is sent an unmark).
type unmarkSender struct {
	mu   sync.Mutex
	cmds []string
	mark string
}

func (u *unmarkSender) SendDaemonCommandToDaemon(_ context.Context, _, _, cmd string, payload []byte, _ int32) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cmds = append(u.cmds, cmd)
	if cmd == worktreereclaim.UnmarkCommand {
		u.mark = ""
	}
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	resp := worktreereclaim.Response{}
	for _, w := range req.Worktrees {
		r := worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeLocked, Landed: w.ProbeLanded,
			Tombstone: &worktreereclaim.TombstoneReport{MarkID: u.mark}}
		if w.State == worktreereclaim.StateArchived {
			r = worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonKept}
		}
		resp.Results = append(resp.Results, r)
	}
	return json.Marshal(resp)
}

// markedFixture is a user with one machine and one live worktree carrying
// reclaim mark "m-1", marked an hour ago.
func markedFixture(t *testing.T) (*inboxFixture, *unmarkSender, string) {
	f, sender, id, _ := markedFixtureSweeper(t)
	return f, sender, id
}

// markedFixtureSweeper is markedFixture with automatic archiving on, and
// its sweeper, so a test can wait for the settles it starts.
func markedFixtureSweeper(t *testing.T) (*inboxFixture, *unmarkSender, string, *worktreesweep.Sweeper) {
	t.Helper()
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: ptr.Of("MacBook")}))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.project(), daemonID, "/home/u/proj", nil))
	now := time.Now().UTC()
	id := "wt-" + uuid.NewString()
	require.NoError(t, f.repo.CreateWorktree(f.ctx, &db.Worktree{
		ID: id, Name: "fix-login", Path: "/home/u/.reliant/worktrees/proj/fix-login", Branch: "feat/x", BaseBranch: "main",
		ProjectID: f.project(), DaemonID: &daemonID, Status: 1, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now, LastActive: now.Add(-48 * time.Hour),
	}))
	ok, err := f.repo.MarkWorktreeForReclaim(f.ctx, id, "m-1", now, nil, db.ArchivedBecauseLanded)
	require.NoError(t, err)
	require.True(t, ok)
	sender := &unmarkSender{mark: "m-1"}
	sweeper := worktreesweep.New(f.repo, sender).WithAutoArchive(worktreesweep.AutoArchiveOn, f.repo, nil).WithReclaimMarks(f.repo, 0, 0)
	f.withSweeper(t, sweeper)
	return f, sender, id, sweeper
}

func (f *inboxFixture) markedItems(t *testing.T) []*reliantv1.InboxItem {
	t.Helper()
	return inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_MARKED)
}

func TestListInbox_MarkedWorktreeIsAnItem(t *testing.T) {
	f, _, id := markedFixture(t)
	items := f.markedItems(t)
	require.Len(t, items, 1)
	it := items[0]
	m := it.GetWorktreeMarked()
	require.NotNil(t, m)
	assert.Equal(t, id, m.WorktreeId)
	assert.Equal(t, "fix-login", m.Name)
	assert.Equal(t, "m-1", m.MarkId)
	assert.Equal(t, "MacBook", m.DaemonName)
	assert.Equal(t, db.ArchivedBecauseLanded, m.Reason)
	assert.False(t, m.Approved)
	markedAt, err := time.Parse(time.RFC3339, m.MarkedAt)
	require.NoError(t, err)
	reclaimAt, err := time.Parse(time.RFC3339, m.ReclaimAt)
	require.NoError(t, err)
	assert.Equal(t, worktreesweep.DefaultReclaimGrace, reclaimAt.Sub(markedAt), "reclaimed a day after the mark")
	assert.Equal(t, markedItemID(id, "m-1"), it.ItemId)
	assert.Equal(t, f.project(), it.ProjectId)

	// Another user never sees it.
	other, _, _ := f.otherUser(t)
	resp, err := f.inbox.ListInbox(other, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	assert.Empty(t, inboxByKind(resp.Msg.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_MARKED))
}

func TestKeepMarkedWorktree(t *testing.T) {
	f, sender, id := markedFixture(t)

	_, err := f.inbox.KeepMarkedWorktree(f.ctx, connect.NewRequest(&reliantv1.KeepMarkedWorktreeRequest{WorktreeId: id, MarkId: "m-old"}))
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "a mark other than the one shown")
	other, _, _ := f.otherUser(t)
	_, err = f.inbox.KeepMarkedWorktree(other, connect.NewRequest(&reliantv1.KeepMarkedWorktreeRequest{WorktreeId: id, MarkId: "m-1"}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "another user's worktree")
	_, err = f.inbox.KeepMarkedWorktree(f.ctx, connect.NewRequest(&reliantv1.KeepMarkedWorktreeRequest{WorktreeId: id}))
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	require.Len(t, f.markedItems(t), 1, "none of those changed it")

	_, err = f.inbox.KeepMarkedWorktree(f.ctx, connect.NewRequest(&reliantv1.KeepMarkedWorktreeRequest{WorktreeId: id, MarkId: "m-1"}))
	require.NoError(t, err)
	assert.Empty(t, f.markedItems(t), "kept: no longer marked")
	assert.Contains(t, sender.cmds, worktreereclaim.UnmarkCommand, "its tombstone removed")
	wt, err := f.repo.GetWorktree(f.ctx, id)
	require.NoError(t, err)
	assert.Nil(t, wt.DeletedAt)
}

func TestReclaimMarkedWorktreeNow(t *testing.T) {
	f, _, id, sweeper := markedFixtureSweeper(t)
	_, err := f.inbox.ReclaimMarkedWorktreeNow(f.ctx, connect.NewRequest(&reliantv1.ReclaimMarkedWorktreeNowRequest{WorktreeId: id, MarkId: "m-old"}))
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	other, _, _ := f.otherUser(t)
	_, err = f.inbox.ReclaimMarkedWorktreeNow(other, connect.NewRequest(&reliantv1.ReclaimMarkedWorktreeNowRequest{WorktreeId: id, MarkId: "m-1"}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	_, err = f.inbox.ReclaimMarkedWorktreeNow(f.ctx, connect.NewRequest(&reliantv1.ReclaimMarkedWorktreeNowRequest{WorktreeId: id, MarkId: "m-1"}))
	require.NoError(t, err)
	sweeper.WaitCleanups()
	// Marked a moment ago, its tombstone standing and its work landed: the
	// approval skipped the day's wait and the background settle reclaimed it.
	wt, err := f.repo.GetWorktree(f.ctx, id)
	require.NoError(t, err)
	require.NotNil(t, wt.DeletedAt, "reclaimed at once")
	assert.Equal(t, db.ArchivedBecauseLanded, wt.CleanupMetadata.ArchivedBecause)
	assert.Empty(t, f.markedItems(t), "no longer a marked live worktree")
}

func TestReclaimMarkedWorktreeNow_StillKeepsAWorktreeWhoseTombstoneIsGone(t *testing.T) {
	f, sender, id, sweeper := markedFixtureSweeper(t)
	sender.mu.Lock()
	sender.mark = "" // someone deleted the tombstone
	sender.mu.Unlock()
	_, err := f.inbox.ReclaimMarkedWorktreeNow(f.ctx, connect.NewRequest(&reliantv1.ReclaimMarkedWorktreeNowRequest{WorktreeId: id, MarkId: "m-1"}))
	require.NoError(t, err)
	sweeper.WaitCleanups()
	wt, err := f.repo.GetWorktree(f.ctx, id)
	require.NoError(t, err)
	assert.Nil(t, wt.DeletedAt, "approval skips the wait, never a check")
	assert.False(t, wt.CleanupMetadata.Marked(), "the mark is cancelled instead")
}
