// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// runningStore is a RunningChatStore answering from a fixed set.
type runningStore struct {
	running      map[string]bool
	err          error
	asked        [][]string
	archiveAsked []string
	archiveOK    bool
	archiveErr   error
}

func (r *runningStore) ArchiveRecoverableIdleWorktree(_ context.Context, id string, _ time.Time) (bool, error) {
	r.archiveAsked = append(r.archiveAsked, id)
	return r.archiveOK, r.archiveErr
}

func (r *runningStore) ListWorktreeIDsWithRunningChat(_ context.Context, ids []string) (map[string]bool, error) {
	r.asked = append(r.asked, ids)
	return r.running, r.err
}

// shedAsks describes every row of store and returns each row's
// ProbeRecoverableAfterSeconds.
func shedAsks(t *testing.T, s *Sweeper, store *fakeStore, keep bool) map[string]int64 {
	t.Helper()
	cands, err := store.ListWorktreesForReclaim(context.Background())
	require.NoError(t, err)
	req, _ := s.describe(context.Background(), cands, nil, keep)
	out := map[string]int64{}
	for _, w := range req.Worktrees {
		out[w.ID] = w.ProbeRecoverableAfterSeconds
	}
	return out
}

// Only a live row with no chat running in it, that is not the main checkout,
// is offered for shedding — and only with the configured idle time.
func TestDescribe_AsksToShedOnlyIdleLiveRows(t *testing.T) {
	t.Parallel()
	store := newFakeStore("d")
	store.add("idle", false, nil)
	store.add("running", false, nil)
	store.add("archived", true, nil)
	store.add("main", false, nil).IsMain = true
	rs := &runningStore{running: map[string]bool{"running": true}}
	s := New(store, &scriptSender{answer: locked}).WithIdleRetirement(36*time.Hour, rs)

	got := shedAsks(t, s, store, false)
	assert.Equal(t, map[string]int64{"idle": 36 * 3600, "running": 0, "archived": 0, "main": 0}, got)
	require.Len(t, rs.asked, 1, "one lookup per batch")
	assert.ElementsMatch(t, []string{"idle", "running"}, rs.asked[0], "only live, non-main rows are looked up")
}

// Nothing is shed when the user keeps everything, when the running-chat
// lookup fails (it cannot tell an agent mid-run from an idle row), or when the
// setting is off.
func TestDescribe_ShedsNothingWhenItCannotTell(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		after time.Duration
		rs    *runningStore
		keep  bool
	}{
		"keep everything": {after: time.Hour, rs: &runningStore{}, keep: true},
		"lookup failed":   {after: time.Hour, rs: &runningStore{err: errors.New("db down")}},
		"off":             {after: 0, rs: &runningStore{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore("d")
			store.add("idle", false, nil)
			s := New(store, &scriptSender{answer: locked}).WithIdleRetirement(tc.after, tc.rs)
			assert.Equal(t, map[string]int64{"idle": 0}, shedAsks(t, s, store, tc.keep))
		})
	}
}

func TestRecoverableIdleArchiveRequiresEnabledModeAndAtomicStoreAck(t *testing.T) {
	for _, mode := range []AutoArchiveMode{AutoArchiveOff, AutoArchiveDryRun, AutoArchiveOn} {
		t.Run(string(mode), func(t *testing.T) {
			store := newFakeStore("d")
			wt := store.add("idle", false, nil)
			rs := &runningStore{archiveOK: true}
			s := New(store, &scriptSender{answer: locked}).WithAutoArchive(mode, store, nil).WithIdleRetirement(time.Hour, rs)
			s.archiveRecoverableIdle(context.Background(), "user", wt)
			if mode == AutoArchiveOn {
				require.Equal(t, []string{"idle"}, rs.archiveAsked)
			} else {
				require.Empty(t, rs.archiveAsked)
			}
		})
	}
}

// A row a restore is still retiring a fence on is not shed in the same request.
func TestDescribe_DoesNotShedARowBeingRestored(t *testing.T) {
	t.Parallel()
	store := newFakeStore("d")
	store.add("restoring", false, nil).CleanupMetadata = &core.CleanupMetadata{RetireFence: "100"}
	s := New(store, &scriptSender{answer: locked}).WithIdleRetirement(time.Hour, &runningStore{})
	assert.Equal(t, map[string]int64{"restoring": 0}, shedAsks(t, s, store, false))
}
