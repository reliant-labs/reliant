// Copyright (c) 2026 Reliant Labs
package daemonstate

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A requested restart is annotated on the record, keeping when its state
// began across repeats of the same state.
func TestSetUpgradeRecordsTheRestartAndKeepsSince(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, Startup{Managed: true}))
	deadline := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Millisecond)

	require.NoError(t, SetUpgrade(dir, &Upgrade{State: "updating", Deadline: deadline, TargetVersion: "v2"}))
	first, err := Read(dir)
	require.NoError(t, err)
	require.NotNil(t, first.Upgrade)
	require.Equal(t, "updating", first.Upgrade.State)
	require.True(t, deadline.Equal(first.Upgrade.Deadline))
	require.False(t, first.Upgrade.Since.IsZero())

	require.NoError(t, SetUpgrade(dir, &Upgrade{State: "updating", Detail: "still"}))
	again, err := Read(dir)
	require.NoError(t, err)
	require.True(t, first.Upgrade.Since.Equal(again.Upgrade.Since), "the same state keeps its start")

	require.NoError(t, SetUpgrade(dir, nil))
	cleared, err := Read(dir)
	require.NoError(t, err)
	require.Nil(t, cleared.Upgrade)
	require.Equal(t, os.Getpid(), cleared.PID, "the rest of the record is untouched")
}

// An upgrade exit leaves the record behind, saying so, where a stop removes
// it; the next daemon's Init starts clean.
func TestMarkUpgradeExitKeepsTheRecordForTheSupervisor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Init(dir, Startup{}))
	require.NoError(t, SetStream(dir, StreamConnected, ""))
	require.NoError(t, SetUpgrade(dir, &Upgrade{State: "drained", TargetVersion: "v2", RolloutID: "r1"}))

	require.NoError(t, MarkUpgradeExit(dir, "exited to update"))
	st, err := Read(dir)
	require.NoError(t, err)
	require.Equal(t, os.Getpid(), st.PID)
	require.Equal(t, StreamDisconnected, st.Stream)
	require.Equal(t, "exited to update", st.StreamDetail)
	require.NotNil(t, st.Upgrade)
	require.Equal(t, UpgradeExited, st.Upgrade.State)
	require.Equal(t, "v2", st.Upgrade.TargetVersion, "the target survives the exit")
	require.Equal(t, "r1", st.Upgrade.RolloutID)

	require.NoError(t, Init(dir, Startup{}))
	restarted, err := Read(dir)
	require.NoError(t, err)
	require.Nil(t, restarted.Upgrade, "the restarted daemon starts with no restart in progress")
}

func TestSetUpgradeWithNoRecordIsInert(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, SetUpgrade(dir, &Upgrade{State: "updating"}))
	_, err := Read(dir)
	require.True(t, os.IsNotExist(err), "no record is invented")
	require.NoError(t, SetUpgrade("", &Upgrade{State: "updating"}))
	require.NoError(t, MarkUpgradeExit("", "x"))
}
