// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

type neverSender struct{ called bool }

func (n *neverSender) SendDaemonCommandToDaemon(context.Context, string, string, string, []byte, int32) ([]byte, error) {
	n.called = true
	return nil, assert.AnError
}

// storageFixture is a user with one daemon, one archived worktree the daemon
// holds as dirty, and a disk report whose build-cache volume is under pressure
// while its home volume is fine (the cloud-machine incident's shape).
func storageFixture(t *testing.T) (*inboxFixture, string, string) {
	t.Helper()
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: ptr.Of("MacBook")}))
	require.NoError(t, f.repo.UpsertDaemonAttachment(f.ctx, &db.DaemonAttachment{DaemonID: daemonID, UserID: f.userID, Source: db.DaemonAttachmentSourceInbound}))

	now := time.Now().UTC()
	wtID := "wt-" + uuid.NewString()
	require.NoError(t, f.repo.CreateWorktree(f.ctx, &db.Worktree{
		ID: wtID, Name: "fix-login", Path: "/home/u/.reliant/worktrees/proj/fix-login", Branch: "feat/x", BaseBranch: "main",
		ProjectID: f.project(), DaemonID: &daemonID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, f.repo.ArchiveWorktree(f.ctx, wtID))
	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, wtID, &db.CleanupMetadata{HeldReason: "dirty", HeldDetail: "2 changed", SizeBytes: 5_000_000}))
	putDiskReport(t, f, daemonID, &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
		{Label: "home", Paths: []string{"/home/u"}, TotalBytes: 157 << 30, FreeBytes: 124 << 30},
		{Label: "build caches & docker", Paths: []string{"/var/cache/workspace/go-build", "/var/lib/docker"}, TotalBytes: 40 << 30, FreeBytes: 1 << 30, Pressure: true,
			Consumers: []*reliantv1.DiskConsumer{{Kind: "docker_images", Label: "Docker images", Category: "docker", Bytes: 18 << 30}}},
	}})
	return f, daemonID, wtID
}

// putDiskReport stores a heartbeat's report the way the gateway does: stamped
// against the report stored before it, so pressure episodes carry forward.
func putDiskReport(t *testing.T, f *inboxFixture, daemonID string, rep *reliantv1.DiskReport) {
	t.Helper()
	prev, err := f.repo.GetDaemonStorageState(f.ctx, daemonID)
	require.NoError(t, err)
	stored, err := diskwatch.Encode(diskwatch.Stamp(rep, diskwatch.Decode(prev), time.Now()))
	require.NoError(t, err)
	require.NoError(t, f.repo.SetDaemonStorageState(f.ctx, daemonID, stored))
}

// putCacheVolume reports the fixture machine's build-cache volume (40 GiB).
func putCacheVolume(t *testing.T, f *inboxFixture, daemonID string, free uint64, pressure bool, noSpaceAtMs int64) {
	t.Helper()
	putDiskReport(t, f, daemonID, &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
		{Label: "build caches & docker", TotalBytes: 40 << 30, FreeBytes: free, Pressure: pressure, LastNoSpaceAtMs: noSpaceAtMs},
	}})
}

func (f *inboxFixture) storage(t *testing.T) []*reliantv1.InboxItem {
	t.Helper()
	return inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE)
}

func (f *inboxFixture) dismiss(t *testing.T, ids ...string) {
	t.Helper()
	_, err := f.inbox.DismissInboxItem(f.ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: ids}))
	require.NoError(t, err)
}

func (f *inboxFixture) holdWorktree(t *testing.T, daemonID, name, reason string) string {
	t.Helper()
	now := time.Now().UTC()
	id := "wt-" + uuid.NewString()
	require.NoError(t, f.repo.CreateWorktree(f.ctx, &db.Worktree{ID: id, Name: name, Path: "/x/" + name, Branch: name, BaseBranch: "main",
		ProjectID: f.project(), DaemonID: &daemonID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, f.repo.ArchiveWorktree(f.ctx, id))
	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, id, &db.CleanupMetadata{HeldReason: reason}))
	return id
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
	require.Len(t, s.Volumes, 2)
	assert.Equal(t, "build caches & docker", s.Volumes[0].Label, "the full volume leads, not the home volume")
	assert.True(t, s.Volumes[0].Pressure)
	assert.EqualValues(t, 40<<30, s.Volumes[0].TotalBytes)
	require.Len(t, s.Volumes[0].Consumers, 1)
	assert.Equal(t, "Docker images", s.Volumes[0].Consumers[0].Label)
	assert.NotEmpty(t, s.ReportedAt)
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
	resp, err := f.inbox.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{ProjectId: ptr.Of("some-other-project")}))
	require.NoError(t, err)
	assert.Len(t, inboxByKind(resp.Msg.Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE), 1)
}

func TestListInbox_NoStorageItemWhenDiskIsFineAndNothingHeld(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))
	putDiskReport(t, f, daemonID, &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
		{Label: "home", TotalBytes: 1000 << 30, FreeBytes: 800 << 30},
	}})
	assert.Empty(t, inboxByKind(f.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE))
}

func TestListInbox_StorageItemIsScopedToItsOwner(t *testing.T) {
	_, _, _ = storageFixture(t)
	other := newInboxFixture(t)
	assert.Empty(t, inboxByKind(other.list(t, nil).Items, reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE))
}

// The user's complaint: "too many inbox notifications about storage". The
// item's id used to hash the whole picture, so every archived branch kept, every
// clean-up, every halving of free space — better or worse — was a new item
// that a dismissal did not cover. A dismissal now holds through all of that.
func TestStorageItem_DismissalHoldsWhileThePictureChurns(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	first := f.storage(t)
	require.Len(t, first, 1)
	f.dismiss(t, first[0].ItemId)
	require.Empty(t, f.storage(t))

	f.holdWorktree(t, daemonID, "another-branch", "unpushed")
	assert.Empty(t, f.storage(t), "archiving another unpushed branch is not a new storage problem")

	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, wtID, &db.CleanupMetadata{HeldReason: "in_use"}))
	assert.Empty(t, f.storage(t), "a kept workspace's reason changing is not a new problem")

	putCacheVolume(t, f, daemonID, 1<<29, true, 0)
	assert.Empty(t, f.storage(t), "free space drifting within the same severity is not news")

	putCacheVolume(t, f, daemonID, 3<<30, true, 0)
	assert.Empty(t, f.storage(t), "the disk getting better must never re-raise the item")

	_, err := f.repo.DB.ExecContext(f.ctx, `DELETE FROM daemon_attachment WHERE daemon_id = $1`, daemonID)
	require.NoError(t, err)
	assert.Empty(t, f.storage(t), "the machine going offline is not news")
}

// Within one episode the item comes back once per climb toward failing
// builds, and never on the way down.
func TestStorageItem_ReturnsOnlyWhenTheDiskGetsWorse(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))

	putCacheVolume(t, f, daemonID, 3<<30, true, 0)
	low := f.storage(t)
	require.Len(t, low, 1)
	assert.True(t, strings.HasSuffix(low[0].ItemId, ":low"), low[0].ItemId)
	f.dismiss(t, low[0].ItemId)
	require.Empty(t, f.storage(t))

	putCacheVolume(t, f, daemonID, 1<<30, true, 0)
	critical := f.storage(t)
	require.Len(t, critical, 1, "below the free floor the next build may not fit: the user hears once more")
	assert.True(t, strings.HasSuffix(critical[0].ItemId, ":critical"), critical[0].ItemId)
	assert.Equal(t, strings.TrimSuffix(low[0].ItemId, ":low"), strings.TrimSuffix(critical[0].ItemId, ":critical"), "same machine, same episode")
	assert.Equal(t, low[0].WaitingSince, critical[0].WaitingSince, "the item dates from when the pressure began")
	f.dismiss(t, critical[0].ItemId)

	putCacheVolume(t, f, daemonID, 3<<30, true, 0)
	assert.Empty(t, f.storage(t), "easing back to low is covered by the critical dismissal")
	putCacheVolume(t, f, daemonID, 1<<30, true, 0)
	assert.Empty(t, f.storage(t), "flapping back to critical is the state already dismissed")

	putCacheVolume(t, f, daemonID, 1<<30, true, time.Now().UnixMilli())
	failing := f.storage(t)
	require.Len(t, failing, 1, "commands failing with ENOSPC always reaches the user")
	assert.True(t, strings.HasSuffix(failing[0].ItemId, ":failing"), failing[0].ItemId)
}

// The disk recovering ends the episode; filling up again later is new. The
// kept workspaces left behind in between were in the dismissed item, so they
// do not come back as news, and Undo brings both back.
func TestStorageItem_ANewPressureEpisodeIsANewItem(t *testing.T) {
	f, daemonID, _ := storageFixture(t)
	first := f.storage(t)
	require.Len(t, first, 1)
	f.dismiss(t, first[0].ItemId)

	putCacheVolume(t, f, daemonID, 12<<30, false, 0)
	assert.Empty(t, f.storage(t), "the disk recovered: the kept workspaces alone are not news, the user saw them")

	time.Sleep(2 * time.Millisecond) // episodes are millisecond-stamped
	putCacheVolume(t, f, daemonID, 1<<30, true, 0)
	again := f.storage(t)
	require.Len(t, again, 1, "the disk filling up again is a new problem")
	assert.NotEqual(t, first[0].ItemId, again[0].ItemId)

	_, err := f.inbox.RestoreInboxItem(f.ctx, connect.NewRequest(&reliantv1.RestoreInboxItemRequest{ItemIds: []string{first[0].ItemId}}))
	require.NoError(t, err)
	putCacheVolume(t, f, daemonID, 12<<30, false, 0)
	kept := f.storage(t)
	require.Len(t, kept, 1, "Undo restores what the dismissal implied")
	assert.False(t, kept[0].GetStorage().DiskLow)
	assert.Len(t, kept[0].GetStorage().Held, 1)
}

// Kept workspaces on a healthy disk are told once per machine. Keeping more is
// not a storage problem; the disk coming under pressure is.
func TestStorageItem_KeptWorkspacesAloneAreToldOnce(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))
	f.holdWorktree(t, daemonID, "one", "dirty")
	putCacheVolume(t, f, daemonID, 30<<30, false, 0)

	kept := f.storage(t)
	require.Len(t, kept, 1)
	assert.Equal(t, "storage:"+daemonID+":kept", kept[0].ItemId)
	f.dismiss(t, kept[0].ItemId)

	f.holdWorktree(t, daemonID, "two", "unpushed")
	f.holdWorktree(t, daemonID, "three", "unpushed")
	assert.Empty(t, f.storage(t), "more kept workspaces on a healthy disk is not news")

	putCacheVolume(t, f, daemonID, 3<<30, true, 0)
	low := f.storage(t)
	require.Len(t, low, 1, "the disk coming under pressure is")
	assert.Len(t, low[0].GetStorage().Held, 3, "and it lists every kept workspace with Clean up")
}

// One machine is one item, however many of its volumes are full.
func TestStorageItem_OneItemPerMachineAcrossVolumes(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID}))
	putDiskReport(t, f, daemonID, &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
		{Label: "home", TotalBytes: 100 << 30, FreeBytes: 5 << 30, Pressure: true},
		{Label: "build caches & docker", TotalBytes: 40 << 30, FreeBytes: 1 << 30, Pressure: true},
	}})
	items := f.storage(t)
	require.Len(t, items, 1)
	assert.True(t, strings.HasSuffix(items[0].ItemId, ":critical"), "graded by the worst volume: %s", items[0].ItemId)
}

func TestStorageItemIDs(t *testing.T) {
	daemonID := "machine:with:colons"
	low := storageItemID(daemonID, diskwatch.SeverityLow, 1760000000000)
	assert.Equal(t, "storage:machine:with:colons:disk:1760000000000:low", low)
	gotDaemon, episode, sev, ok := parseStorageDiskID(low)
	require.True(t, ok)
	assert.Equal(t, daemonID, gotDaemon)
	assert.EqualValues(t, 1760000000000, episode)
	assert.Equal(t, diskwatch.SeverityLow, sev)

	kept := storageItemID(daemonID, diskwatch.SeverityNone, 0)
	_, _, _, ok = parseStorageDiskID(kept)
	assert.False(t, ok, "the kept item is not a disk item")
	for _, bad := range []string{"approval:x", "storage:d:disk:abc:low", "storage:d:disk:1:worse", "storage:d:disk:1"} {
		_, _, _, ok = parseStorageDiskID(bad)
		assert.False(t, ok, bad)
	}

	assert.Equal(t, []string{
		low,
		storageItemID(daemonID, diskwatch.SeverityCritical, 1760000000000),
		storageItemID(daemonID, diskwatch.SeverityFailing, 1760000000000),
	}, dismissalKeys(low), "a dismissal at any severity at or above the current one hides it")
	failing := storageItemID(daemonID, diskwatch.SeverityFailing, 1760000000000)
	assert.Equal(t, []string{failing}, dismissalKeys(failing))
	assert.Equal(t, []string{kept}, dismissalKeys(kept))
	assert.Equal(t, []string{"approval:x"}, dismissalKeys("approval:x"))

	assert.Equal(t, []string{low, failing, kept}, impliedDismissals([]string{low, failing}))
	assert.Equal(t, []string{kept, "approval:x"}, impliedDismissals([]string{kept, "approval:x"}))
}

func TestCleanupStorage_RejectsAnotherUsersMachineAndAskedNothing(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	sender := &neverSender{}
	f.withSweeper(t, worktreesweep.New(f.repo, sender))

	otherCtx, _, _ := f.otherUser(t)
	_, err := f.inbox.CleanupStorage(otherCtx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.False(t, sender.called, "another user's machine must never be contacted")

	_, err = f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

func TestCleanupStorage_OfflineMachineIsAPreconditionNotAnError(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	_, err := f.repo.DB.ExecContext(f.ctx, `DELETE FROM daemon_attachment WHERE daemon_id = $1`, daemonID)
	require.NoError(t, err)
	f.withSweeper(t, worktreesweep.New(f.repo, &neverSender{}))
	_, err = f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
}

func TestCleanupStorage_UnavailableWithoutASweeper(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	_, err := f.inbox.CleanupStorage(f.ctx, connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}}))
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
}

// heldSender does not answer until the test releases it, so an RPC that waits
// for the machine cannot return while it is held. Its context is recorded so
// the test can see the work outlived the request's.
type heldSender struct {
	release chan struct{}
	done    chan struct{}
	ctxErr  error
}

func (s *heldSender) SendDaemonCommandToDaemon(ctx context.Context, _, _, _ string, _ []byte, _ int32) ([]byte, error) {
	defer close(s.done)
	<-s.release
	s.ctxErr = ctx.Err()
	return nil, assert.AnError
}

// S6: Clean up goes through the REAL deadline slot and budget table. It must return
// accepted at once, and the work must outlive the request's context.
func TestCleanupStorage_ReturnsAtOnceAndSurvivesTheRequestContext(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	sender := &heldSender{release: make(chan struct{}), done: make(chan struct{})}
	f.withSweeper(t, worktreesweep.New(f.repo, sender))

	ic := serverkit.Deadline{Unary: interceptors.UnaryTimeout, Procedures: interceptors.ProcedureTimeouts()}.WrapUnary
	reqCtx, cancelReq := context.WithCancel(f.ctx)
	call := ic(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return f.inbox.CleanupStorage(ctx, req.(*connect.Request[reliantv1.CleanupStorageRequest]))
	})
	req := connect.NewRequest(&reliantv1.CleanupStorageRequest{DaemonId: daemonID, WorktreeIds: []string{wtID}})

	// The machine is held for the whole call: an RPC that waits for it never
	// returns, and fails here rather than by a wall-clock bound a loaded host
	// can miss.
	type result struct {
		resp connect.AnyResponse
		err  error
	}
	returned := make(chan result, 1)
	go func() {
		resp, err := call(reqCtx, req)
		returned <- result{resp, err}
	}()
	var res result
	select {
	case res = <-returned:
	case <-time.After(10 * time.Second):
		close(sender.release)
		t.Fatal("the RPC waited for the machine")
	}
	require.NoError(t, res.err)
	out := res.resp.(*connect.Response[reliantv1.CleanupStorageResponse]).Msg
	require.Len(t, out.Results, 1)
	assert.Equal(t, reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_ACCEPTED, out.Results[0].Outcome)

	// The request is over; the clean-up must outlive its context.
	cancelReq()
	close(sender.release)
	select {
	case <-sender.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the background clean-up never ran")
	}
	assert.NoError(t, sender.ctxErr, "the clean-up ran on the request's cancelled context")
	require.Eventually(t, func() bool {
		wt, err := f.repo.GetWorktree(f.ctx, wtID)
		return err == nil && wt.CleanupMetadata != nil && !wt.CleanupMetadata.Cleaning
	}, 5*time.Second, 50*time.Millisecond, "a failed clean-up clears its in-progress flag")
}

func TestCleanupStorage_NotRemovableWorktreesAreSkippedNotAccepted(t *testing.T) {
	f, daemonID, wtID := storageFixture(t)
	require.NoError(t, f.repo.UpdateWorktreeCleanupMetadata(f.ctx, wtID, &db.CleanupMetadata{HeldReason: "data"}))
	sender := &neverSender{}
	f.withSweeper(t, worktreesweep.New(f.repo, sender))
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

func (s *Service) mustList(t *testing.T, f *inboxFixture) []*reliantv1.InboxItem {
	t.Helper()
	resp, err := s.ListInbox(f.ctx, connect.NewRequest(&reliantv1.ListInboxRequest{}))
	require.NoError(t, err)
	return resp.Msg.Items
}

// A machine near its process limit gets an item of its own, naming the
// largest process trees by the agent that started each — by chat title for
// the caller's own chats, never another user's — and no storage item when the
// disk is fine and nothing is kept.
func TestListInbox_ProcessLimitItemNamesTheTreesByAgent(t *testing.T) {
	f := newInboxFixture(t)
	daemonID := "d-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: daemonID, UserID: f.userID, Hostname: ptr.Of("cloud")}))
	now := time.Now().UTC()
	wf, wid := "wf", "c-gate"
	require.NoError(t, f.repo.CreateChat(f.ctx, &db.Chat{
		ID: "c-gate", Title: "Release gate under 1h", ProjectID: f.project(), UserID: f.userID, WorkflowName: &wf, WorkflowID: &wid,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	otherCtx, otherUser, otherProject := f.otherUser(t)
	owid := "c-theirs"
	require.NoError(t, f.repo.CreateChat(otherCtx, &db.Chat{
		ID: "c-theirs", Title: "someone else's", ProjectID: otherProject, UserID: otherUser, WorkflowName: &wf, WorkflowID: &owid,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	report := func(refusing bool) *reliantv1.DiskReport {
		return &reliantv1.DiskReport{
			Volumes: []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 1000 << 30, FreeBytes: 800 << 30}},
			Processes: &reliantv1.ProcessReport{Count: 5400, Limit: 7650, RefusingHeavy: refusing, Top: []*reliantv1.ProcessTree{
				{ChatId: "c-gate", Command: "bash scripts/ci-gate.sh all", Tasks: 4950, Tracked: true},
				{ChatId: "c-theirs", Command: "go test ./...", Tasks: 200, Tracked: true},
				{Command: "nohup /tmp/blitz/gate.sh 1631", Tasks: 120},
			}},
		}
	}
	putDiskReport(t, f, daemonID, report(true))

	items := f.storage(t)
	require.Len(t, items, 1, "the process item only: the disk is fine and nothing is kept")
	assert.True(t, strings.HasPrefix(items[0].ItemId, "storage:"+daemonID+":procs:"), items[0].ItemId)
	p := items[0].GetStorage().GetProcesses()
	require.NotNil(t, p)
	assert.True(t, p.RefusingHeavy)
	assert.EqualValues(t, 5400, p.Count)
	require.Len(t, p.Top, 3)
	assert.Equal(t, "Release gate under 1h", p.Top[0].ChatTitle)
	assert.Empty(t, p.Top[1].ChatTitle, "another user's chat is not named")
	assert.Equal(t, "nohup /tmp/blitz/gate.sh 1631", p.Top[2].Command)

	// Dismissed, it stays dismissed for the episode; recovered, it is gone.
	f.dismiss(t, items[0].ItemId)
	assert.Empty(t, f.storage(t))
	putDiskReport(t, f, daemonID, report(false))
	assert.Empty(t, f.storage(t))
}
