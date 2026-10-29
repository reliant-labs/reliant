// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// A queued message remembers the machine the run that queued it waited on, and
// the delivery record bounds the retries: attempts count, back off, give up,
// and come back for one more try; the user's own settle forgets all of it.
func TestChatQueuedDeliveryRecord(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("q-1", at)))

	// A chat queued before records existed has none: no machine of its own.
	_, err := store.SetChatQueuedForMachine(ctx, env.ORM, "q-1", true)
	require.NoError(t, err)
	listed, err := store.ListChatsQueuedForMachine(ctx, env.ORM, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, listed[0].Target.IsZero(), "no record: the chat's own machine")
	assert.Zero(t, listed[0].Attempts)

	// Queueing records the machine the run waited on, and why it gave up.
	queued, err := env.Repo.QueueChatForMachine(ctx, "q-1", core.QueuedMachineTarget{DaemonName: "cicd", Labels: map[string]string{"pool": "ci"}}, "the machine is not connected")
	require.NoError(t, err)
	assert.False(t, queued, "the marker was already set")
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, core.QueuedMachineTarget{DaemonName: "cicd", Labels: map[string]string{"pool": "ci"}}, listed[0].Target)
	assert.Equal(t, "the machine is not connected", listed[0].LastFailure)

	// Attempts count and hold the next one off.
	next := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "q-1", next))
	require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "q-1", next))
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, 2, listed[0].Attempts)
	require.NotNil(t, listed[0].NextAttemptAt)
	assert.WithinDuration(t, next, *listed[0].NextAttemptAt, time.Second)

	// Queueing again — the same loop, one more turn — keeps the count: it is
	// what bounds the loop.
	_, err = env.Repo.QueueChatForMachine(ctx, "q-1", core.QueuedMachineTarget{DaemonName: "cicd"}, "still not connected")
	require.NoError(t, err)
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	assert.Equal(t, 2, listed[0].Attempts)
	assert.Equal(t, "still not connected", listed[0].LastFailure)

	// Giving up needs the attempts spent: with tries left it does nothing.
	early, err := env.Repo.GiveUpQueuedDelivery(ctx, "q-1")
	require.NoError(t, err)
	assert.False(t, early)
	for range core.MaxQueuedDeliveryAttempts - 2 {
		require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "q-1", next))
	}

	// Giving up clears the queued marker (the chat reads as a failed run, not
	// as waiting) and parks the record; only the first caller is told.
	stopped, err := env.Repo.GiveUpQueuedDelivery(ctx, "q-1")
	require.NoError(t, err)
	assert.True(t, stopped)
	again, err := env.Repo.GiveUpQueuedDelivery(ctx, "q-1")
	require.NoError(t, err)
	assert.False(t, again, "of two replicas giving up, one tells the user")
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	assert.Empty(t, listed, "given up: the sweep no longer sees it")
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, true)
	require.NoError(t, err)
	assert.Empty(t, listed, "inside the hold not even a connect sees it: a flapping machine cannot revive it at once")
	_, err = env.SQL.Exec(`UPDATE chat_queued_deliveries SET next_attempt_at = NOW() - interval '1 second' WHERE chat_id = 'q-1'`)
	require.NoError(t, err)
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, true)
	require.NoError(t, err)
	require.Len(t, listed, 1, "once the hold has passed, the connect path does")
	assert.NotNil(t, listed[0].GaveUpAt)
	chat, err := env.Repo.GetChat(ctx, "q-1")
	require.NoError(t, err)
	assert.NotEqual(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, reliantv1.ChatActivity(*chat.Activity), "the chat no longer reads as waiting")

	// The machine it waits for connecting revives it for exactly one more try.
	revived, err := env.Repo.ReviveQueuedDelivery(ctx, "q-1")
	require.NoError(t, err)
	assert.True(t, revived)
	again, err = env.Repo.ReviveQueuedDelivery(ctx, "q-1")
	require.NoError(t, err)
	assert.False(t, again)
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1, "queued again, and visible to the sweep")
	assert.Equal(t, core.MaxQueuedDeliveryAttempts-1, listed[0].Attempts, "one attempt short of the limit, not a fresh budget")
	assert.Equal(t, core.QueuedMachineTarget{DaemonName: "cicd"}, listed[0].Target, "it still waits for the same machine")

	// Settling — a run got past preflight, or the user sent a message —
	// forgets the record with the marker.
	settled, err := env.Repo.SettleQueuedDelivery(ctx, "q-1")
	require.NoError(t, err)
	assert.True(t, settled)
	var n int
	require.NoError(t, env.SQL.QueryRow(`SELECT count(*) FROM chat_queued_deliveries WHERE chat_id = 'q-1'`).Scan(&n))
	assert.Zero(t, n)
}

// A delivery record from an outage that ended long ago does not count against
// an unrelated one.
func TestChatQueuedDeliveryRecord_StaleStreakStartsOver(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("q-2", at)))

	_, err := env.Repo.QueueChatForMachine(ctx, "q-2", core.QueuedMachineTarget{DaemonID: "d1"}, "down")
	require.NoError(t, err)
	require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "q-2", time.Now()))
	_, err = env.SQL.Exec(`UPDATE chat_queued_deliveries SET updated_at = NOW() - interval '3 hours' WHERE chat_id = 'q-2'`)
	require.NoError(t, err)

	_, err = env.Repo.QueueChatForMachine(ctx, "q-2", core.QueuedMachineTarget{DaemonID: "d1"}, "down again")
	require.NoError(t, err)

	listed, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Zero(t, listed[0].Attempts, "the earlier outage is over")
	assert.Nil(t, listed[0].NextAttemptAt)
}

func recordsFor(t *testing.T, env *storetest.Env, chatID string) int {
	t.Helper()
	var n int
	require.NoError(t, env.SQL.QueryRow(`SELECT count(*) FROM chat_queued_deliveries WHERE chat_id = $1`, chatID).Scan(&n))
	return n
}

// A give-up that loses the race with the owner's send must not recreate the
// stop state. The send settles (deletes the record, clears the marker); a
// give-up that was already in flight used to INSERT a fresh parked row, so the
// user saw a spurious notice and their next message — queued fresh — inherited
// gave_up_at and was skipped.
func TestChatQueuedDelivery_AGiveUpThatLosesTheRaceWithTheOwnersSettleChangesNothing(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("r-1", at)))
	target := core.QueuedMachineTarget{DaemonName: "cicd"}

	_, err := env.Repo.QueueChatForMachine(ctx, "r-1", target, "down")
	require.NoError(t, err)
	for range core.MaxQueuedDeliveryAttempts {
		require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "r-1", time.Now()))
	}

	// The owner sends: settled.
	_, err = env.Repo.SettleQueuedDelivery(ctx, "r-1")
	require.NoError(t, err)
	require.Zero(t, recordsFor(t, env, "r-1"))

	// The give-up that read the chat before the settle lands now.
	stopped, err := env.Repo.GiveUpQueuedDelivery(ctx, "r-1")
	require.NoError(t, err)
	assert.False(t, stopped, "nothing to give up on: the owner already settled it, so no notice")
	assert.Zero(t, recordsFor(t, env, "r-1"), "and no parked record is recreated")

	// An attempt that was in flight cannot bring the record back either.
	require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "r-1", time.Now()))
	assert.Zero(t, recordsFor(t, env, "r-1"))

	// The owner's next message, queued fresh, is retried, not skipped.
	_, err = env.Repo.QueueChatForMachine(ctx, "r-1", target, "down again")
	require.NoError(t, err)
	listed, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Nil(t, listed[0].GaveUpAt)
	assert.Zero(t, listed[0].Attempts)
}

// Giving up needs the attempts to have been spent: a stale reader that only
// thinks they were cannot park a record that has tries left.
func TestChatQueuedDelivery_GiveUpRequiresTheAttemptsToBeSpent(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("r-2", at)))
	_, err := env.Repo.QueueChatForMachine(ctx, "r-2", core.QueuedMachineTarget{DaemonName: "cicd"}, "down")
	require.NoError(t, err)
	for range core.MaxQueuedDeliveryAttempts - 1 {
		require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "r-2", time.Now()))
	}

	stopped, err := env.Repo.GiveUpQueuedDelivery(ctx, "r-2")
	require.NoError(t, err)
	assert.False(t, stopped)
	chat, err := env.Repo.GetChat(ctx, "r-2")
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, reliantv1.ChatActivity(*chat.Activity), "still queued, still being retried")
}

// A message queued again after a give-up (the run delivery started failed once
// more) is queued again: the parked record is un-parked with it.
func TestChatQueuedDelivery_QueueingAgainUnparksTheRecord(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("r-3", at)))
	target := core.QueuedMachineTarget{DaemonName: "cicd"}
	_, err := env.Repo.QueueChatForMachine(ctx, "r-3", target, "down")
	require.NoError(t, err)
	for range core.MaxQueuedDeliveryAttempts {
		require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "r-3", time.Now()))
	}
	stopped, err := env.Repo.GiveUpQueuedDelivery(ctx, "r-3")
	require.NoError(t, err)
	require.True(t, stopped)

	_, err = env.Repo.QueueChatForMachine(ctx, "r-3", target, "down once more")
	require.NoError(t, err)

	listed, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1, "queued, and visible to the sweep")
	assert.Nil(t, listed[0].GaveUpAt)
	assert.Equal(t, core.MaxQueuedDeliveryAttempts, listed[0].Attempts, "the spent attempts are not forgiven; the next sweep gives up again")
}

// A parked record is listed only after its hold, and after the queued ones.
func TestChatQueuedDelivery_AParkedRecordIsListedOnlyOnceItsHoldHasPassed(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("r-4", at)))
	_, err := env.Repo.QueueChatForMachine(ctx, "r-4", core.QueuedMachineTarget{DaemonName: "cicd"}, "down")
	require.NoError(t, err)
	for range core.MaxQueuedDeliveryAttempts {
		require.NoError(t, env.Repo.RecordQueuedDeliveryAttempt(ctx, "r-4", time.Now()))
	}
	stopped, err := env.Repo.GiveUpQueuedDelivery(ctx, "r-4")
	require.NoError(t, err)
	require.True(t, stopped)

	held, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, true)
	require.NoError(t, err)
	assert.Empty(t, held, "inside the hold nothing is listed, even for the connect path")

	_, err = env.SQL.Exec(`UPDATE chat_queued_deliveries SET next_attempt_at = NOW() - interval '1 second' WHERE chat_id = 'r-4'`)
	require.NoError(t, err)
	after, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, true)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.NotNil(t, after[0].GaveUpAt)
	hidden, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	assert.Empty(t, hidden, "the listing that does not ask for them never returns them")
}

// The stored failure is cut on a character boundary, never in the middle of one.
func TestChatQueuedDelivery_TruncatesTheFailureOnARuneBoundary(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	require.NoError(t, store.InsertChat(ctx, env.ORM, convChat("r-5", at)))
	failure := strings.Repeat("é", 400) // 800 bytes, every byte pair one character

	_, err := env.Repo.QueueChatForMachine(ctx, "r-5", core.QueuedMachineTarget{DaemonName: "cicd"}, failure)
	require.NoError(t, err)

	listed, err := env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, utf8.ValidString(listed[0].LastFailure))
	assert.LessOrEqual(t, len(listed[0].LastFailure), 500)
	assert.Equal(t, strings.Repeat("é", 250), listed[0].LastFailure)

	// A three-byte character straddling the limit is dropped whole.
	_, err = env.Repo.QueueChatForMachine(ctx, "r-5", core.QueuedMachineTarget{DaemonName: "cicd"}, strings.Repeat("a", 499)+"€")
	require.NoError(t, err)
	listed, err = env.Repo.ListChatsQueuedForMachine(ctx, "", 10, false)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 499), listed[0].LastFailure)
}
