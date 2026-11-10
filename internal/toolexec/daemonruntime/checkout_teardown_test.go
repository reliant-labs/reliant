// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTeardownNow is the fixed load time of the journals these tests open.
var testTeardownNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func newTestTeardownJournal(t *testing.T) (*teardownJournal, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), checkoutTeardownDirName)
	j, err := loadTeardownJournal(dir, testTeardownNow)
	require.NoError(t, err)
	return j, dir
}

// A retry, or a redelivery, of an op the daemon already ran answers its
// outcome and never deletes again.
func TestTeardownRunsAnOpAtMostOncePerID(t *testing.T) {
	t.Parallel()
	j, _ := newTestTeardownJournal(t)
	var runs atomic.Int32
	fn := func(context.Context) (bool, error) { runs.Add(1); return true, nil }

	first, err := j.run(context.Background(), "op-1", "remove", "/clone", fn)
	require.NoError(t, err)
	assert.Equal(t, teardownDone, first.State)
	assert.True(t, first.Removed)

	again, err := j.run(context.Background(), "op-1", "remove", "/clone", fn)
	require.NoError(t, err)
	assert.Equal(t, teardownDone, again.State)
	assert.True(t, again.Removed, "the retry is told what the first delivery did")
	assert.EqualValues(t, 1, runs.Load())
}

// A second delivery that arrives while the first is still deleting waits for
// it instead of running beside it.
func TestTeardownDuplicateWhileRunningWaitsForTheFirst(t *testing.T) {
	t.Parallel()
	j, _ := newTestTeardownJournal(t)
	release := make(chan struct{})
	var runs atomic.Int32
	fn := func(context.Context) (bool, error) { runs.Add(1); <-release; return true, nil }

	firstDone := make(chan teardownRecord)
	go func() {
		rec, _ := j.run(context.Background(), "op-2", "reclone", "/clone", fn)
		firstDone <- rec
	}()
	require.Eventually(t, func() bool { return runs.Load() == 1 }, 5*time.Second, time.Millisecond)

	secondDone := make(chan teardownRecord)
	go func() {
		rec, _ := j.run(context.Background(), "op-2", "reclone", "/clone", fn)
		secondDone <- rec
	}()
	close(release)
	assert.Equal(t, teardownDone, (<-firstDone).State)
	assert.Equal(t, teardownDone, (<-secondDone).State)
	assert.EqualValues(t, 1, runs.Load())
}

// The server stops waiting; the op does not stop with it. The request answers
// running, and a later status query answers how it ended.
func TestTeardownOutlivesTheRequestThatStartedIt(t *testing.T) {
	t.Parallel()
	j, _ := newTestTeardownJournal(t)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()
	rec, err := j.run(ctx, "op-3", "reclone", "/clone", func(opCtx context.Context) (bool, error) {
		close(started)
		<-release
		return true, opCtx.Err()
	})
	require.NoError(t, err)
	assert.Equal(t, teardownRunning, rec.State)

	close(release)
	require.Eventually(t, func() bool {
		st, err := j.status("op-3")
		return err == nil && st.State == teardownDone
	}, 5*time.Second, time.Millisecond, "the op finished on its own context, not the request's")
}

// A status query for an op never received fences it: a delivery still in
// flight then refuses to run, so "not run" stays true.
func TestTeardownStatusFencesAnUnknownOp(t *testing.T) {
	t.Parallel()
	j, dir := newTestTeardownJournal(t)
	st, err := j.status("op-4")
	require.NoError(t, err)
	assert.Equal(t, teardownFenced, st.State)

	ran := false
	late, err := j.run(context.Background(), "op-4", "remove", "/clone", func(context.Context) (bool, error) { ran = true; return true, nil })
	require.NoError(t, err)
	assert.Equal(t, teardownFenced, late.State)
	assert.False(t, ran, "a fenced op never runs")

	// The tombstone is durable: a restarted daemon still refuses it.
	reloaded, err := loadTeardownJournal(dir, testTeardownNow)
	require.NoError(t, err)
	late, err = reloaded.run(context.Background(), "op-4", "remove", "/clone", func(context.Context) (bool, error) { ran = true; return true, nil })
	require.NoError(t, err)
	assert.Equal(t, teardownFenced, late.State)
	assert.False(t, ran)
}

// An op the last daemon left running is interrupted after a restart: nothing
// runs it any more, and it is not run again.
func TestTeardownRunningAtRestartIsInterrupted(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), checkoutTeardownDirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	data, _ := json.Marshal(teardownRecord{OpID: "op-5", Kind: "remove", Path: "/clone", State: teardownRunning, UpdatedAt: testTeardownNow})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op-5.json"), data, 0o600))

	j, err := loadTeardownJournal(dir, testTeardownNow)
	require.NoError(t, err)
	st, err := j.status("op-5")
	require.NoError(t, err)
	assert.Equal(t, teardownInterrupted, st.State)

	ran := false
	rec, err := j.run(context.Background(), "op-5", "remove", "/clone", func(context.Context) (bool, error) { ran = true; return true, nil })
	require.NoError(t, err)
	assert.Equal(t, teardownInterrupted, rec.State)
	assert.False(t, ran)
}

// An outcome outlives the daemon: a status query after a restart answers it.
func TestTeardownOutcomeSurvivesARestart(t *testing.T) {
	t.Parallel()
	j, dir := newTestTeardownJournal(t)
	_, err := j.run(context.Background(), "op-6", "remove", "/clone", func(context.Context) (bool, error) { return false, assert.AnError })
	require.NoError(t, err)

	reloaded, err := loadTeardownJournal(dir, testTeardownNow)
	require.NoError(t, err)
	st, err := reloaded.status("op-6")
	require.NoError(t, err)
	assert.Equal(t, teardownFailed, st.State)
	assert.Equal(t, assert.AnError.Error(), st.Error)

}

// Records are never collected: a server can retry an op whose answer was
// lost, and a fenced op's delivery can arrive, any time later. Forgotten,
// either would run as new — a second delete, or a fenced op running after
// the server released workspace creation.
func TestTeardownRecordsOutliveAnyRetryOrLateDelivery(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), checkoutTeardownDirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	longAgo := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, rec := range []teardownRecord{
		{OpID: "op-done", Kind: "remove", Path: "/clone", State: teardownDone, Removed: true, UpdatedAt: longAgo},
		{OpID: "op-fenced", State: teardownFenced, UpdatedAt: longAgo},
	} {
		data, _ := json.Marshal(rec)
		require.NoError(t, os.WriteFile(filepath.Join(dir, rec.OpID+".json"), data, 0o600))
	}
	j, err := loadTeardownJournal(dir, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	ran := false
	fn := func(context.Context) (bool, error) { ran = true; return true, nil }
	retry, err := j.run(context.Background(), "op-done", "remove", "/clone", fn)
	require.NoError(t, err)
	assert.Equal(t, teardownDone, retry.State, "a retry years later is answered, not run")
	late, err := j.run(context.Background(), "op-fenced", "remove", "/clone", fn)
	require.NoError(t, err)
	assert.Equal(t, teardownFenced, late.State, "a fenced op's late delivery is refused years later")
	assert.False(t, ran)
}

// A journal that cannot be loaded is never stood in for by a memory-only
// one: acknowledged teardowns and fences are refused (nothing is deleted,
// nothing falsely fenced) until a daemon loads the journal, and then the ids
// it already held are honoured again.
func TestTeardownsAreRefusedUntilTheJournalLoads(t *testing.T) {
	prev, prevErr := checkoutTeardowns.j, checkoutTeardowns.err
	t.Cleanup(func() { setTeardownJournal(prev, prevErr) })

	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, checkoutTeardownDirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	clone := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".git"), 0o755))
	data, _ := json.Marshal(teardownRecord{OpID: "op-known", Kind: "remove", State: teardownDone, Path: clone, UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op-known.json"), data, 0o600))
	// An unreadable record: the journal cannot vouch for every id it saw.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op-torn.json"), []byte("{not json"), 0o600))

	initializeCheckoutTeardowns(context.Background(), dataDir)
	remove, _ := json.Marshal(gitRemoveRequest{Path: clone, OpID: "op-known"})
	_, err := handleGitRemove(context.Background(), remove)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot record removes and reclones durably")
	assert.DirExists(t, clone, "nothing is deleted without the durable journal")
	status, _ := json.Marshal(gitTeardownStatusRequest{OpID: "op-unseen"})
	_, err = handleGitTeardownStatus(context.Background(), status)
	require.Error(t, err, "nothing is fenced without the durable journal either")

	// The "restart" with a readable journal: the known op is answered, not
	// run, and the id never seen is fenced durably.
	require.NoError(t, os.Remove(filepath.Join(dir, "op-torn.json")))
	initializeCheckoutTeardowns(context.Background(), dataDir)
	out, err := handleGitRemove(context.Background(), remove)
	require.NoError(t, err)
	var resp gitRemoveResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.Equal(t, teardownDone, resp.OpState)
	assert.DirExists(t, clone, "the known op already ran; it is not run again")
	out, err = handleGitTeardownStatus(context.Background(), status)
	require.NoError(t, err)
	var st gitTeardownStatusResponse
	require.NoError(t, json.Unmarshal(out, &st))
	assert.Equal(t, teardownFenced, st.OpState)
	assert.FileExists(t, filepath.Join(dir, "op-unseen.json"))

	// No data dir at all is refused the same way.
	initializeCheckoutTeardowns(context.Background(), "")
	_, err = handleGitRemove(context.Background(), remove)
	assert.Error(t, err)
}

var installTestTeardownJournalOnce sync.Once

// installTestTeardownJournal gives the handlers a durable journal, shared by
// the parallel handler tests (they use distinct op ids).
func installTestTeardownJournal(t *testing.T) {
	t.Helper()
	installTestTeardownJournalOnce.Do(func() {
		dir, err := os.MkdirTemp("", "teardown-journal-")
		require.NoError(t, err)
		j, err := loadTeardownJournal(dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		setTeardownJournal(j, nil)
	})
}

// Nothing is deleted unless the op is journaled first.
func TestTeardownRefusesToRunWithoutAJournal(t *testing.T) {
	t.Parallel()
	j, dir := newTestTeardownJournal(t)
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, nil, 0o600)) // a file where the directory was
	ran := false
	_, err := j.run(context.Background(), "op-7", "remove", "/clone", func(context.Context) (bool, error) { ran = true; return true, nil })
	require.Error(t, err)
	assert.False(t, ran)
}

func TestTeardownOpIDNamesOneTeardown(t *testing.T) {
	t.Parallel()
	j, _ := newTestTeardownJournal(t)
	_, err := j.run(context.Background(), "op-8", "remove", "/a", func(context.Context) (bool, error) { return true, nil })
	require.NoError(t, err)
	_, err = j.run(context.Background(), "op-8", "reclone", "/a", func(context.Context) (bool, error) { return true, nil })
	assert.ErrorIs(t, err, errTeardownMismatch)
	_, err = j.run(context.Background(), "../../etc", "remove", "/a", func(context.Context) (bool, error) { return true, nil })
	assert.Error(t, err, "an op id is a file name")
}

// git.remove with an op id acknowledges it; without one (an older server) it
// answers exactly as before.
func TestGitRemoveAcknowledgesAnOpID(t *testing.T) {
	t.Parallel()
	installTestTeardownJournal(t)
	clone := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".git"), 0o755))
	opID := uuid.NewString()
	payload, _ := json.Marshal(gitRemoveRequest{Path: clone, OpID: opID})

	out, err := handleGitRemove(context.Background(), payload)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.Equal(t, map[string]any{"success": true, "removed": true, "op_id": opID, "op_state": "done"}, resp)
	assert.NoDirExists(t, clone)

	// A redelivery after a new clone landed at the path must not delete it.
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".git"), 0o755))
	_, err = handleGitRemove(context.Background(), payload)
	require.NoError(t, err)
	assert.DirExists(t, clone, "the op already ran; its redelivery deletes nothing")

	legacy, _ := json.Marshal(gitRemoveRequest{Path: clone})
	out, err = handleGitRemove(context.Background(), legacy)
	require.NoError(t, err)
	resp = nil
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.Equal(t, map[string]any{"success": true, "removed": true}, resp, "no op id: the old reply, nothing added")
}

func TestGitTeardownStatusAnswersAndFences(t *testing.T) {
	t.Parallel()
	installTestTeardownJournal(t)
	opID := uuid.NewString()
	payload, _ := json.Marshal(gitTeardownStatusRequest{OpID: opID})
	out, err := handleGitTeardownStatus(context.Background(), payload)
	require.NoError(t, err)
	var resp gitTeardownStatusResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.Equal(t, gitTeardownStatusResponse{OpID: opID, OpState: teardownFenced}, resp)

	clone := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".git"), 0o755))
	remove, _ := json.Marshal(gitRemoveRequest{Path: clone, OpID: opID})
	out, err = handleGitRemove(context.Background(), remove)
	require.NoError(t, err)
	var removeResp gitRemoveResponse
	require.NoError(t, json.Unmarshal(out, &removeResp))
	assert.Equal(t, teardownFenced, removeResp.OpState)
	assert.False(t, removeResp.Success)
	assert.DirExists(t, clone, "the late delivery of a fenced op deletes nothing")
}
