// Copyright (c) 2026 Reliant Labs
package inbox

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// reclaimMachine is a user with one connected machine whose disk is fine.
func reclaimMachine(t *testing.T) (*inboxFixture, string) {
	t.Helper()
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: ptr.Of("default")}))
	require.NoError(t, f.repo.UpsertDaemonAttachment(f.ctx, &db.DaemonAttachment{DaemonID: daemonID, UserID: f.userID, Source: db.DaemonAttachmentSourceInbound}))
	return f, daemonID
}

func reclaimProposal(key, fp, decision string, proposedAt time.Time) *reliantv1.WorktreeReclaimProposal {
	return &reliantv1.WorktreeReclaimProposal{Key: key, Fingerprint: fp, Path: "/tmp/" + key, Branch: "feat/" + key, Reason: "dirty",
		WorkSummary: " M a.txt", IdleSeconds: 6 * 86400, SizeBytes: 1 << 20, ProposedAtMs: proposedAt.UnixMilli(), Decision: decision}
}

func putProposals(t *testing.T, f *inboxFixture, daemonID string, proposals ...*reliantv1.WorktreeReclaimProposal) {
	t.Helper()
	putDiskReport(t, f, daemonID, &reliantv1.DiskReport{
		Volumes:         []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 100 << 30, FreeBytes: 80 << 30}},
		WorktreeReclaim: &reliantv1.WorktreeReclaimProposals{Mode: "on", Total: int32(len(proposals)), Proposals: proposals},
	})
}

func (f *inboxFixture) reclaimItems(t *testing.T) []*reliantv1.InboxItem {
	t.Helper()
	return inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM)
}

// A machine's proposals are one Inbox item, listing every undecided
// worktree and any already approved, not the declined ones; with a healthy
// disk there is no storage item beside it.
func TestListInbox_WorktreeReclaimItemListsTheMachinesProposals(t *testing.T) {
	f, daemonID := reclaimMachine(t)
	old := time.Now().Add(-3 * time.Hour)
	putProposals(t, f, daemonID,
		reclaimProposal("p-a", "fp-a", "", time.Now()),
		reclaimProposal("p-b", "fp-b", "", old),
		reclaimProposal("p-c", "fp-c", "approve", old),
		reclaimProposal("p-d", "fp-d", "decline", old))

	items := f.reclaimItems(t)
	require.Len(t, items, 1)
	assert.Empty(t, f.storage(t), "a healthy disk raises no storage item")
	it := items[0]
	r := it.GetWorktreeReclaim()
	require.NotNil(t, r)
	assert.Equal(t, daemonID, r.DaemonId)
	assert.Equal(t, "default", r.DaemonName)
	assert.True(t, r.Online)
	assert.Equal(t, "on", r.Mode)
	keys := []string{}
	for _, p := range r.Proposals {
		keys = append(keys, p.Key)
	}
	assert.Equal(t, []string{"p-a", "p-b", "p-c"}, keys)
	assert.Equal(t, old.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano), it.WaitingSince, "waiting since the oldest undecided proposal")
	assert.Contains(t, it.ItemId, "worktree_reclaim:"+daemonID+":")

	// Project-scoped lists show it too: a machine is not any project's.
	scoped, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{ProjectId: ptr.Of(f.project())}))
	require.NoError(t, err)
	assert.Len(t, inboxByKind(scoped.Msg.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM), 1)
}

// The same undecided set is the same item however often the machine reports
// it, so a dismissal holds; a new proposal, or a worktree whose state
// changed, is a new item; once every proposal is decided, there is none.
func TestListInbox_WorktreeReclaimItemIsOnePerSetOfProposals(t *testing.T) {
	f, daemonID := reclaimMachine(t)
	now := time.Now()
	putProposals(t, f, daemonID, reclaimProposal("p-a", "fp-a", "", now))
	first := f.reclaimItems(t)
	require.Len(t, first, 1)

	putProposals(t, f, daemonID, func() *reliantv1.WorktreeReclaimProposal {
		p := reclaimProposal("p-a", "fp-a", "", now)
		p.IdleSeconds++ // only time passing
		return p
	}())
	again := f.reclaimItems(t)
	require.Len(t, again, 1)
	assert.Equal(t, first[0].ItemId, again[0].ItemId, "the same proposals are the same item")

	f.dismiss(t, first[0].ItemId)
	assert.Empty(t, f.reclaimItems(t), "dismissed")

	putProposals(t, f, daemonID, reclaimProposal("p-a", "fp-a2", "", now))
	changed := f.reclaimItems(t)
	require.Len(t, changed, 1, "a worktree whose state changed asks again")
	assert.NotEqual(t, first[0].ItemId, changed[0].ItemId)

	putProposals(t, f, daemonID, reclaimProposal("p-a", "fp-a2", "approve", now), reclaimProposal("p-b", "fp-b", "decline", now))
	assert.Empty(t, f.reclaimItems(t), "nothing left to decide")
}
