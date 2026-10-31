// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// fakeStore is an in-memory Store, so these tests exercise the sweep's
// scheduling and bookkeeping without a database or a filesystem.
type fakeStore struct {
	mu         sync.Mutex
	rows       map[string]*core.ReclaimCandidate
	order      []string
	chats      []*core.Chat
	chatErr    error
	archiveErr error
	archived   []string
	daemon     string
	// disk is each daemon's stored disk report (diskwatch.Encode).
	disk map[string]string
	// activity is each worktree's chat runs (WorktreeRunActivities); a row
	// with no entry has had nothing happen in it since the epoch.
	activity    map[string]core.WorktreeRunActivity
	activityErr error
}

func newFakeStore(daemon string) *fakeStore {
	return &fakeStore{rows: map[string]*core.ReclaimCandidate{}, daemon: daemon}
}

func (f *fakeStore) add(id string, archived bool, checkedAt *time.Time) *core.Worktree {
	d := f.daemon
	wt := &core.Worktree{ID: id, Name: id, Path: "/w/" + id, ProjectID: "p", DaemonID: &d, BaseBranch: "main"}
	if archived {
		at := time.Unix(1_700_000_000, 0).UTC()
		wt.DeletedAt = &at
	}
	if checkedAt != nil {
		wt.CleanupMetadata = &core.CleanupMetadata{CheckedAt: checkedAt}
	}
	f.rows[id] = &core.ReclaimCandidate{Worktree: wt, OwnerUserID: "u", ProjectPath: "/proj"}
	f.order = append(f.order, id)
	return wt
}

func (f *fakeStore) ListWorktreesForReclaim(context.Context) ([]*core.ReclaimCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*core.ReclaimCandidate
	for _, id := range f.order {
		out = append(out, f.rows[id])
	}
	return out, nil
}
func (f *fakeStore) ListLiveWorktreePathsForUser(context.Context, string) ([]core.WorktreePath, error) {
	return nil, nil
}
func (f *fakeStore) MergeWorktreeCleanupMetadata(_ context.Context, id string, fn func(*core.CleanupMetadata, bool) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	wt := f.rows[id].Worktree
	m := core.CleanupMetadata{}
	if wt.CleanupMetadata != nil {
		m = *wt.CleanupMetadata
	}
	if fn(&m, wt.DeletedAt != nil) {
		wt.CleanupMetadata = &m
	}
	return nil
}
func (f *fakeStore) GetWorktree(_ context.Context, id string) (*core.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.rows[id].Worktree
	return &cp, nil
}
func (f *fakeStore) ListReposByProject(context.Context, string) ([]*core.Repo, error) {
	return nil, nil
}
func (f *fakeStore) UpdateWorktreeCleanupMetadata(context.Context, string, *core.CleanupMetadata) error {
	return nil
}
func (f *fakeStore) AdoptWorktreeDaemon(_ context.Context, _ string, daemonID string) (string, error) {
	return daemonID, nil
}
func (f *fakeStore) ListAttachedDaemonIDsForUser(context.Context, string, time.Duration) ([]string, error) {
	return []string{f.daemon}, nil
}
func (f *fakeStore) ListDaemonStorageState(context.Context, string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disk, nil
}
func (f *fakeStore) TryAdvisoryLock(context.Context, int64) (func(), bool, error) {
	return func() {}, true, nil
}

// AutoArchiveStore.
func (f *fakeStore) GetLiveChatIDForWorktree(_ context.Context, id string) (string, error) {
	if f.chatErr != nil {
		return "", f.chatErr
	}
	for _, c := range f.chats {
		if c.WorktreeID != nil && *c.WorktreeID == id && c.State != core.ChatStateArchived {
			return c.ID, nil
		}
	}
	return "", nil
}
func (f *fakeStore) ArchiveWorktree(_ context.Context, id string) error {
	if f.archiveErr != nil {
		return f.archiveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived = append(f.archived, id)
	at := time.Now().UTC()
	f.rows[id].Worktree.DeletedAt = &at
	return nil
}

func (f *fakeStore) WorktreeRunActivities(_ context.Context, ids []string) (map[string]core.WorktreeRunActivity, error) {
	if f.activityErr != nil {
		return nil, f.activityErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]core.WorktreeRunActivity{}
	for _, id := range ids {
		out[id] = f.activity[id]
	}
	return out, nil
}
func (f *fakeStore) ArchiveLandedWorktree(_ context.Context, id string, quietSince time.Time) (bool, error) {
	if f.archiveErr != nil {
		return false, f.archiveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	wt := f.rows[id].Worktree
	if a := f.activity[id]; wt.DeletedAt != nil || wt.IsMain || a.Running || a.LastActive.After(quietSince) {
		return false, nil
	}
	f.archived = append(f.archived, id)
	at := time.Now().UTC()
	wt.DeletedAt = &at
	m := core.CleanupMetadata{}
	if wt.CleanupMetadata != nil {
		m = *wt.CleanupMetadata
	}
	m.ArchivedBecause = core.ArchivedBecauseLanded
	wt.CleanupMetadata = &m
	return true, nil
}

// scriptSender answers each worktree from a script and can fail any request
// that contains a chosen worktree, the way a daemon that timed out does.
type scriptSender struct {
	mu       sync.Mutex
	failWith map[string]bool
	answer   func(w worktreereclaim.Worktree) worktreereclaim.Result
	requests [][]string
	probed   map[string]bool
	// seen is the last description of each worktree the daemon was sent.
	seen map[string]worktreereclaim.Worktree
}

func (s *scriptSender) SendDaemonCommandToDaemon(_ context.Context, _, _, _ string, payload []byte, _ int32) ([]byte, error) {
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, w := range req.Worktrees {
		ids = append(ids, w.ID)
	}
	s.requests = append(s.requests, ids)
	for _, w := range req.Worktrees {
		if s.failWith[w.ID] {
			return nil, errors.New("daemon command timed out")
		}
	}
	resp := worktreereclaim.Response{}
	for _, w := range req.Worktrees {
		if s.probed != nil {
			s.probed[w.ID] = w.ProbeIdle
		}
		if s.seen != nil {
			s.seen[w.ID] = w
		}
		resp.Results = append(resp.Results, s.answer(w))
	}
	return json.Marshal(resp)
}

func locked(w worktreereclaim.Worktree) worktreereclaim.Result {
	return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeLocked}
}

func TestSweep_OneSlowWorktreeDoesNotStarveTheBatchBehindIt(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	for _, id := range []string{"a", "b", "c", "slow", "e", "f", "g", "h"} {
		store.add(id, false, nil)
	}
	sender := &scriptSender{failWith: map[string]bool{"slow": true}, answer: locked}
	s := New(store, sender)

	require.NoError(t, s.Sweep(context.Background()))

	for _, id := range []string{"a", "b", "c", "e", "f", "g", "h"} {
		meta := store.rows[id].Worktree.CleanupMetadata
		require.NotNil(t, meta, "%s was never settled: one failing worktree abandoned the whole batch", id)
		assert.NotNil(t, meta.LockedAt, id)
	}
	slow := store.rows["slow"].Worktree.CleanupMetadata
	require.NotNil(t, slow, "the worktree that failed must be recorded so it backs off")
	assert.Equal(t, "error", slow.Transient)
	assert.NotNil(t, slow.CheckedAt)
}

func TestSweep_RepeatedFailureBecomesAVisibleHold(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	store.add("stuck", true, nil)
	sender := &scriptSender{failWith: map[string]bool{"stuck": true}, answer: locked}
	s := New(store, sender)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	for i := 0; i < transientToError+1; i++ {
		require.NoError(t, s.Sweep(context.Background()))
		now = now.Add(transientBackoffMax + time.Minute) // each pass is due again
	}
	meta := store.rows["stuck"].Worktree.CleanupMetadata
	require.NotNil(t, meta)
	assert.Equal(t, string(worktreereclaim.ReasonError), meta.HeldReason, "the Inbox must show a worktree the machine keeps failing on")
}

func TestSweep_AFailedWorktreeBacksOffInsteadOfStayingDue(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	store.add("slow", true, nil)
	sender := &scriptSender{failWith: map[string]bool{"slow": true}, answer: locked}
	s := New(store, sender)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	require.NoError(t, s.Sweep(context.Background()))
	first := len(sender.requests)
	require.Positive(t, first)

	now = now.Add(time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Len(t, sender.requests, first, "a worktree that just failed must not be asked again a minute later")

	now = now.Add(transientBackoffMin + time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Greater(t, len(sender.requests), first, "but it is asked again once the backoff passes")
}

func TestSweep_TransientOutcomesAreRecordedNotDropped(t *testing.T) {
	t.Parallel()

	for _, reason := range []worktreereclaim.HeldReason{worktreereclaim.ReasonRecentlyActive, worktreereclaim.ReasonStale} {
		t.Run(string(reason), func(t *testing.T) {
			store := newFakeStore("d")
			store.add("w", true, nil)
			sender := &scriptSender{answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
				return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeHeld, Reason: reason}
			}}
			s := New(store, sender)
			now := time.Now().UTC()
			s.now = func() time.Time { return now }

			require.NoError(t, s.Sweep(context.Background()))
			meta := store.rows["w"].Worktree.CleanupMetadata
			require.NotNil(t, meta, "a transient answer used to leave no trace, so the row was due on every pass")
			assert.Equal(t, string(reason), meta.Transient)
			assert.NotNil(t, meta.CheckedAt)
			assert.Empty(t, meta.HeldReason, "and it is not a hold the user should see")
			assert.False(t, s.due(store.rows["w"].Worktree, time.Time{}))
		})
	}
}

func TestSweep_DaemonErrorIsRecordedAsAHold(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	store.add("w", true, nil)
	sender := &scriptSender{answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
		return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeError, Error: "git refused"}
	}}
	require.NoError(t, New(store, sender).Sweep(context.Background()))
	meta := store.rows["w"].Worktree.CleanupMetadata
	require.NotNil(t, meta)
	assert.Equal(t, string(worktreereclaim.ReasonError), meta.HeldReason)
	assert.Equal(t, "git refused", meta.HeldDetail)
}

func TestSweep_LeastRecentlyCheckedFirst_ArchivedBeforeLive(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	old := time.Now().Add(-48 * time.Hour).UTC()
	recent := time.Now().Add(-time.Hour).UTC()
	store.add("live-new", false, nil)
	store.add("arch-recent", true, &recent)
	store.add("arch-never", true, nil)
	store.add("arch-old", true, &old)

	var order []string
	sender := &scriptSender{answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
		order = append(order, w.ID)
		return locked(w)
	}}
	require.NoError(t, New(store, sender).Sweep(context.Background()))
	assert.Equal(t, []string{"arch-never", "arch-old", "arch-recent", "live-new"}, order,
		"archived rows (disk owed back) first, never-checked before long-ago before recent")
}

func TestSweep_ABatchThatFailsEverywhereStopsAskingThatDaemon(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	fail := map[string]bool{}
	for i := 0; i < 40; i++ {
		id := string(rune('A'+i/26)) + string(rune('a'+i%26))
		store.add(id, true, nil)
		fail[id] = true
	}
	sender := &scriptSender{failWith: fail, answer: locked}
	require.NoError(t, New(store, sender).Sweep(context.Background()))
	assert.LessOrEqual(t, len(sender.requests), maxFailedSends, "a dead daemon must not be hammered with bisected requests")
}

func autoEnv(t *testing.T, mode AutoArchiveMode, chats []*core.Chat) (*fakeStore, *scriptSender, *Sweeper) {
	t.Helper()
	store := newFakeStore("d")
	store.chats = chats
	store.add("done", false, nil)
	sender := &scriptSender{probed: map[string]bool{}, answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
		return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeIdleDone}
	}}
	var notified []string
	s := New(store, sender).WithAutoArchive(mode, store, func(_ context.Context, _, _, id string) { notified = append(notified, id) })
	t.Cleanup(func() {
		if mode == AutoArchiveOn && len(store.archived) > 0 {
			assert.Equal(t, store.archived, notified, "clients are told the list changed")
		}
	})
	return store, sender, s
}

func TestAutoArchive_OnArchivesAFinishedWorktree(t *testing.T) {
	t.Parallel()

	store, sender, s := autoEnv(t, AutoArchiveOn, nil)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"done"}, store.archived)
	assert.True(t, sender.probed["done"], "the daemon is asked to probe idleness")
}

func TestAutoArchive_DryRunReportsAndArchivesNothing(t *testing.T) {
	t.Parallel()

	store, _, s := autoEnv(t, AutoArchiveDryRun, nil)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Empty(t, store.archived)
	assert.EqualValues(t, 1, s.candidates.Load(), "the candidate count is what the dry run exists to show")
	assert.EqualValues(t, 0, s.archived.Load())
}

func TestAutoArchive_OffNeverProbes(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	store.add("done", false, nil)
	sender := &scriptSender{probed: map[string]bool{}, answer: locked}
	s := New(store, sender).WithAutoArchive(AutoArchiveOff, store, nil)
	require.NoError(t, s.Sweep(context.Background()))
	assert.False(t, sender.probed["done"])
	assert.Empty(t, store.archived)
}

func TestAutoArchive_ALiveChatKeepsTheWorktree(t *testing.T) {
	t.Parallel()

	id := "done"
	store, _, s := autoEnv(t, AutoArchiveOn, []*core.Chat{{ID: "c", WorktreeID: &id}})
	require.NoError(t, s.Sweep(context.Background()))
	assert.Empty(t, store.archived, "a worktree a live chat is bound to is never archived")
}

func TestAutoArchive_MoreThan1000UnrelatedChatsDoNotBlock(t *testing.T) {
	t.Parallel()

	chats := make([]*core.Chat, 1001)
	for i := range chats {
		other := "other"
		chats[i] = &core.Chat{ID: "c", WorktreeID: &other}
	}
	store, _, s := autoEnv(t, AutoArchiveOn, chats)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"done"}, store.archived, "unrelated chats cannot hide an unused worktree")
	assert.EqualValues(t, 1, s.idleDone.Load())
	assert.EqualValues(t, 0, s.blockedByChat.Load())
}

func TestAutoArchive_StoreErrorsAreCounted(t *testing.T) {
	t.Parallel()
	for _, lookup := range []bool{true, false} {
		store, _, s := autoEnv(t, AutoArchiveOn, nil)
		if lookup {
			store.chatErr = errors.New("lookup failed")
		} else {
			store.archiveErr = errors.New("archive failed")
		}
		require.NoError(t, s.Sweep(context.Background()))
		assert.Empty(t, store.archived)
		assert.EqualValues(t, 1, s.idleDone.Load())
		assert.EqualValues(t, 1, s.archiveErrors.Load())
	}
}

func TestAutoArchive_NeverArchivesTheMainCheckout(t *testing.T) {
	t.Parallel()

	store, _, s := autoEnv(t, AutoArchiveOn, nil)
	store.rows["done"].Worktree.IsMain = true
	require.NoError(t, s.Sweep(context.Background()))
	assert.Empty(t, store.archived)
}

func TestAutoArchive_KeepEverythingUserIsNeverProbed(t *testing.T) {
	t.Parallel()

	store, sender, s := autoEnv(t, AutoArchiveOn, nil)
	s.WithKeepFiles(keepAll{})
	require.NoError(t, s.Sweep(context.Background()))
	assert.False(t, sender.probed["done"])
	assert.Empty(t, store.archived, `"Keep everything" means nothing is archived on the user's behalf`)
}

func TestParseAutoArchiveMode_DefaultNeverArchives(t *testing.T) {
	t.Parallel()

	store, _, s := autoEnv(t, ParseAutoArchiveMode(""), nil)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Empty(t, store.archived, "an unset switch must only report candidates")
	assert.EqualValues(t, 1, s.candidates.Load())
}

func TestParseAutoArchiveMode(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]AutoArchiveMode{
		"": AutoArchiveDryRun, "on": AutoArchiveOn, " ON ": AutoArchiveOn, "dry-run": AutoArchiveDryRun,
		"off": AutoArchiveOff, "false": AutoArchiveOff, "onn": AutoArchiveOff, "0": AutoArchiveOff,
	} {
		assert.Equal(t, want, ParseAutoArchiveMode(in), "%q", in)
	}
}
