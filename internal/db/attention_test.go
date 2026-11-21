package db

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func attentionChat(t *testing.T, r *Repo, id string) {
	t.Helper()
	createActivityTestChat(t, r, id)
	recordLaunchEvent(t, r, id, core.TriggerEventKindSchedule, nil, time.Now().UTC())
}

func attentionApply(t *testing.T, r *Repo, u core.AttentionUpdate) core.AttentionResult {
	t.Helper()
	result, err := r.ApplyAttentionUpdate(context.Background(), u)
	require.NoError(t, err)
	require.True(t, result.Managed)
	return result
}

func attentionList(t *testing.T, r *Repo) []*core.AttentionIncident {
	t.Helper()
	items, err := r.ListAttentionIncidents(context.Background(), "test-user")
	require.NoError(t, err)
	return items
}

func TestAttentionEpisodesAndDurableReplay(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "attention-chat")
	start := time.Now().UTC()
	u := core.AttentionUpdate{ChatID: "attention-chat", RunID: "run-a", StartedAt: start, Phase: "start", Key: "pr:1"}
	require.False(t, attentionApply(t, r, u).Notify)
	require.Empty(t, attentionList(t, r), "no-transition start only establishes a fence")
	u.Phase, u.State = "finish", "action_required"
	u.FailureClass, u.Detail = "unsuccessful", "repair needed"
	require.True(t, attentionApply(t, r, u).Notify)
	items := attentionList(t, r)
	require.Len(t, items, 1)
	episode := items[0].EpisodeID
	require.NotEmpty(t, episode)
	require.Equal(t, "unsuccessful", items[0].FailureClass)
	require.NoError(t, r.DismissInboxItems(context.Background(), "test-user", []string{"automation_failing:incident:" + episode}, time.Now().UTC()))
	require.False(t, attentionApply(t, r, u).Notify, "finish replay is idempotent")
	// Newer observer's success does not resolve, but fences the old actor.
	observer := core.AttentionUpdate{ChatID: u.ChatID, RunID: "run-b", StartedAt: start.Add(time.Second), Phase: "finish", Key: u.Key}
	attentionApply(t, r, observer)
	require.Len(t, attentionList(t, r), 1)
	repair := core.AttentionUpdate{ChatID: u.ChatID, RunID: "run-c", StartedAt: start.Add(2 * time.Second), Phase: "start", Key: u.Key, State: "recovering", RecoveryTimeout: time.Hour}
	attentionApply(t, r, repair)
	items = attentionList(t, r)
	require.Equal(t, episode, items[0].EpisodeID)
	repair.Phase, repair.State, repair.RecoveryTimeout = "finish", "resolved", 0
	attentionApply(t, r, repair)
	require.Empty(t, attentionList(t, r))
	// Neither delayed terminal nor delayed start can reopen the tombstone.
	attentionApply(t, r, u)
	u.Phase, u.State = "start", "action_required"
	attentionApply(t, r, u)
	require.Empty(t, attentionList(t, r))
	fresh := core.AttentionUpdate{ChatID: u.ChatID, RunID: "run-d", StartedAt: start.Add(3 * time.Second), Phase: "finish", Key: u.Key, State: "action_required"}
	require.True(t, attentionApply(t, r, fresh).Notify)
	items = attentionList(t, r)
	require.Len(t, items, 1)
	require.NotEqual(t, episode, items[0].EpisodeID)
	dismissed, err := r.ListDismissedInboxItemIDs(context.Background(), "test-user", []string{"automation_failing:incident:" + episode, "automation_failing:incident:" + items[0].EpisodeID})
	require.NoError(t, err)
	require.True(t, dismissed["automation_failing:incident:"+episode])
	require.False(t, dismissed["automation_failing:incident:"+items[0].EpisodeID])
}

func TestAttentionFenceAndStaleRegistration(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "fence-old")
	attentionChat(t, r, "fence-new")
	at := time.Now().UTC().Truncate(time.Microsecond)
	newer := core.AttentionUpdate{ChatID: "fence-new", RunID: "z", StartedAt: at, Phase: "start", Key: "key"}
	attentionApply(t, r, newer)
	older := core.AttentionUpdate{ChatID: "fence-old", RunID: "a", StartedAt: at, Phase: "finish", Key: "key", State: "action_required"}
	require.False(t, attentionApply(t, r, older).Notify)
	require.Empty(t, attentionList(t, r))
	// The newer no-transition start has no durable failure backstop yet.
	// Complete it before treating it as a safely managed legacy firing.
	newer.Phase = "finish"
	attentionApply(t, r, newer)
	managed, err := r.AttentionManagedChats(context.Background(), "test-user", []string{older.ChatID, newer.ChatID, "missing"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{older.ChatID: true, newer.ChatID: true}, managed)
	managed, err = r.AttentionManagedChats(context.Background(), "other-user", []string{older.ChatID, newer.ChatID})
	require.NoError(t, err)
	require.Empty(t, managed)
	// Preserve sub-microsecond Temporal ordering, not postgres rounding.
	nsNew := older
	nsNew.RunID = "0"
	nsNew.StartedAt = at.Add(time.Nanosecond)
	require.True(t, attentionApply(t, r, nsNew).Notify)
	require.Len(t, attentionList(t, r), 1)
	// Reusing RunID with a new start or chat is rejected, not reinterpreted.
	bad := nsNew
	bad.StartedAt = at.Add(2 * time.Nanosecond)
	_, err = r.ApplyAttentionUpdate(context.Background(), bad)
	require.ErrorContains(t, err, "immutable")
}

func TestAttentionFinishBeforeStartAndIncompleteRecovery(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "out-of-order")
	u := core.AttentionUpdate{ChatID: "out-of-order", RunID: "terminal-first", StartedAt: time.Now().UTC(), Phase: "finish", Key: "key", State: "action_required"}
	require.True(t, attentionApply(t, r, u).Notify)
	u.Phase, u.State, u.RecoveryTimeout = "start", "recovering", time.Hour
	attentionApply(t, r, u)
	require.Equal(t, "action_required", attentionList(t, r)[0].State)
	u.RunID = "new-repair"
	u.StartedAt = u.StartedAt.Add(time.Second)
	attentionApply(t, r, u)
	items := attentionList(t, r)
	require.Equal(t, "recovering", items[0].State)
	deadline := *items[0].LeaseUntil
	require.WithinDuration(t, u.StartedAt.Add(time.Hour), deadline, time.Microsecond, "lease is based on immutable run start, never activity receipt time")
	// Retry with a different timeout cannot renew the original deadline.
	u.RecoveryTimeout = 2 * time.Hour
	attentionApply(t, r, u)
	require.Equal(t, deadline, *attentionList(t, r)[0].LeaseUntil)
	u.Phase, u.State, u.RecoveryTimeout = "finish", "", 0
	attentionApply(t, r, u)
	require.Equal(t, "action_required", attentionList(t, r)[0].State, "completion with no explicit resolution releases suppression")
}

func TestAttentionReadExpiryAndExpiredOwnerFence(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "expired-owner")
	u := core.AttentionUpdate{ChatID: "expired-owner", RunID: "owner", StartedAt: time.Now().UTC(), Phase: "start", Key: "key", State: "recovering", RecoveryTimeout: time.Hour}
	attentionApply(t, r, u)
	before := attentionList(t, r)
	require.Len(t, before, 1)
	// Move only the stored deadline, not a sleep or the production clock.
	_, err := r.ORM().Exec(context.Background(), `UPDATE attention_incidents SET lease_until=clock_timestamp()-interval '1 second' WHERE user_id=$1`, "test-user")
	require.NoError(t, err)
	after := attentionList(t, r)
	require.Len(t, after, 1)
	require.Equal(t, "action_required", after[0].State)
	require.Equal(t, "recovery_expired", after[0].FailureClass)
	require.Equal(t, before[0].EpisodeID, after[0].EpisodeID)
	require.Nil(t, after[0].LeaseUntil)
	u.Phase, u.State, u.RecoveryTimeout = "finish", "resolved", 0
	attentionApply(t, r, u)
	require.Equal(t, "action_required", attentionList(t, r)[0].State, "expired owner cannot erase the durable incident")
	u.Phase, u.State, u.RecoveryTimeout = "start", "recovering", time.Hour
	attentionApply(t, r, u)
	require.Equal(t, "action_required", attentionList(t, r)[0].State)
	u.RunID = "new-owner"
	u.StartedAt = u.StartedAt.Add(time.Second)
	attentionApply(t, r, u)
	require.Equal(t, "recovering", attentionList(t, r)[0].State)
	u.Phase, u.State, u.RecoveryTimeout = "finish", "resolved", 0
	attentionApply(t, r, u)
	require.Empty(t, attentionList(t, r))
}

func TestAttentionAlreadyExpiredLeaseAndBounds(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "late-start")
	u := core.AttentionUpdate{ChatID: "late-start", RunID: "late", StartedAt: time.Now().Add(-2 * time.Hour), Phase: "start", Key: "key", State: "recovering", RecoveryTimeout: time.Hour}
	require.True(t, attentionApply(t, r, u).Notify)
	require.Equal(t, "recovery_expired", attentionList(t, r)[0].FailureClass)
	u.RecoveryTimeout = 25 * time.Hour
	_, err := r.ApplyAttentionUpdate(context.Background(), u)
	require.ErrorContains(t, err, "timeout")
	u.RecoveryTimeout = time.Millisecond
	_, err = r.ApplyAttentionUpdate(context.Background(), u)
	require.ErrorContains(t, err, "timeout")
}

func TestAttentionOwnershipAndKeyIsolation(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	attentionChat(t, r, "owned")
	createActivityTestChat(t, r, "interactive")
	base := core.AttentionUpdate{ChatID: "interactive", RunID: "invalid", StartedAt: time.Now().UTC(), Phase: "finish", Key: "same-key", State: "action_required"}
	_, err := r.ApplyAttentionUpdate(ctx, base)
	require.Error(t, err)
	base.ChatID = "owned"
	base.RunID = "valid"
	attentionApply(t, r, base)
	base.Key = "unrelated"
	base.RunID = "other-key"
	base.State = "resolved"
	attentionApply(t, r, base)
	require.Len(t, attentionList(t, r), 1, "unrelated key never resolves")
	other, err := r.ListAttentionIncidents(ctx, "other-user")
	require.NoError(t, err)
	require.Empty(t, other)
	// Existing chat with mismatched project owner is not a valid identity.
	_, err = r.ORM().Exec(ctx, `UPDATE projects SET user_id=$1 WHERE id=$2`, "other-user", "test-project")
	require.NoError(t, err)
	base.RunID = "mismatch"
	_, err = r.ApplyAttentionUpdate(ctx, base)
	require.Error(t, err)
	require.Empty(t, attentionList(t, r))
	managed, err := r.AttentionManagedChats(ctx, "test-user", []string{"owned"})
	require.NoError(t, err)
	require.Empty(t, managed)
}

func TestAttentionScopeIdentityAndNanosecondFence(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	at := time.Now().UTC()
	for _, scope := range []struct{ project, user string }{{"project-a", "test-user"}, {"project-b", "test-user"}, {"project-c", "other-user"}} {
		require.NoError(t, r.CreateProject(ctx, &Project{ID: scope.project, UserID: scope.user, Name: scope.project, Path: "/tmp/" + scope.project, CreatedAt: at, UpdatedAt: at, LastActive: at}))
		chat := "chat-" + scope.project
		require.NoError(t, r.CreateChat(ctx, &Chat{ID: chat, UserID: scope.user, ProjectID: scope.project, Title: chat, State: 2, CreatedAt: at, UpdatedAt: at, LastActive: at}))
		_, err := r.CreateTriggerEvent(ctx, &core.TriggerEvent{ID: "event-" + chat, ChatID: &chat, UserID: scope.user, Kind: core.TriggerEventKindSchedule, DedupeKey: chat, OccurredAt: at, CreatedAt: at, Outcome: core.TriggerEventLaunched})
		require.NoError(t, err)
		setTestChatOrigin(t, r, chat, core.ChatOriginAutomation)
		attentionApply(t, r, core.AttentionUpdate{ChatID: chat, RunID: chat, StartedAt: at, Phase: "finish", Key: "same-key", State: "action_required"})
	}
	mine := attentionList(t, r)
	require.Len(t, mine, 2)
	require.NotEqual(t, mine[0].ProjectID, mine[1].ProjectID)
	other, err := r.ListAttentionIncidents(ctx, "other-user")
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.Equal(t, "project-c", other[0].ProjectID)
	attentionApply(t, r, core.AttentionUpdate{ChatID: "chat-project-a", RunID: "resolve-a", StartedAt: at.Add(time.Second), Phase: "finish", Key: "same-key", State: "resolved"})
	mine = attentionList(t, r)
	require.Len(t, mine, 1)
	require.Equal(t, "project-b", mine[0].ProjectID)
	other, err = r.ListAttentionIncidents(ctx, "other-user")
	require.NoError(t, err)
	require.Len(t, other, 1, "another tenant cannot be resolved by same-key success")
}

func TestAttentionNoTransitionActorDoesNotRenewLease(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "lease-fence")
	at := time.Now().UTC()
	u := core.AttentionUpdate{ChatID: "lease-fence", RunID: "recovering", StartedAt: at, Phase: "start", Key: "key", State: "recovering", RecoveryTimeout: time.Hour}
	attentionApply(t, r, u)
	initial := attentionList(t, r)[0]
	observer := core.AttentionUpdate{ChatID: u.ChatID, RunID: "observer", StartedAt: at.Add(time.Second), Phase: "start", Key: u.Key}
	attentionApply(t, r, observer)
	after := attentionList(t, r)[0]
	require.Equal(t, initial.EpisodeID, after.EpisodeID)
	require.Equal(t, *initial.LeaseUntil, *after.LeaseUntil)
	u.Phase, u.State, u.RecoveryTimeout = "finish", "resolved", 0
	attentionApply(t, r, u)
	require.Equal(t, "recovering", attentionList(t, r)[0].State, "new no-op observer fences older completion, without renewing suppression")
}

func TestAttentionManagedFailSafeOnLostTerminal(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	at := time.Now().UTC()
	managed := func(chat string) bool {
		t.Helper()
		m, err := r.AttentionManagedChats(ctx, "test-user", []string{chat})
		require.NoError(t, err)
		return m[chat]
	}
	for _, state := range []string{"", "recovering", "action_required"} {
		chat := "lost-finish-" + state
		attentionChat(t, r, chat)
		u := core.AttentionUpdate{ChatID: chat, RunID: chat, StartedAt: at, Phase: "start", Key: chat, State: state}
		if state == "recovering" {
			u.RecoveryTimeout = time.Hour
		}
		attentionApply(t, r, u)
		require.Equal(t, state != "", managed(chat), "lost observer finish must fall back to legacy; explicit starts have a durable backstop")
		if state == "recovering" {
			_, err := r.ORM().Exec(ctx, `UPDATE attention_incidents SET lease_until=clock_timestamp()-interval '1 second' WHERE incident_key=$1`, chat)
			require.NoError(t, err)
			require.True(t, managed(chat), "read-time expiry keeps durable action-required backstop")
		}
		u.Phase, u.State, u.RecoveryTimeout = "finish", "action_required", 0
		attentionApply(t, r, u)
		require.True(t, managed(chat), "even stale/expired terminal receipts remain registered")
	}
	// Newer successful tombstone fences an explicit but stale start. Since
	// that start cannot open an episode, its missing terminal remains legacy.
	attentionChat(t, r, "stale-explicit")
	newer := core.AttentionUpdate{ChatID: "stale-explicit", RunID: "new", StartedAt: at.Add(time.Second), Phase: "finish", Key: "resolved-key", State: "resolved"}
	attentionApply(t, r, newer)
	older := newer
	older.RunID, older.StartedAt, older.Phase, older.State, older.RecoveryTimeout = "old", at, "start", "recovering", time.Hour
	attentionApply(t, r, older)
	// Use a separate chat so the newer finished actor doesn't make the same
	// launch chat managed independently of the stale start.
	attentionChat(t, r, "stale-other-chat")
	older.ChatID, older.RunID = "stale-other-chat", "old-other"
	attentionApply(t, r, older)
	require.False(t, managed(older.ChatID))
	older.Phase, older.State, older.RecoveryTimeout = "finish", "action_required", 0
	attentionApply(t, r, older)
	require.True(t, managed(older.ChatID), "finished stale actors never leak legacy alerts")
}

func TestAttentionInvalidUpdatesNeverRegister(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "invalid-policy")
	base := core.AttentionUpdate{ChatID: "invalid-policy", RunID: "invalid-run", StartedAt: time.Now().UTC(), Phase: "start", Key: "key"}
	for _, mutate := range []func(*core.AttentionUpdate){
		func(u *core.AttentionUpdate) { u.Key = " " },
		func(u *core.AttentionUpdate) { u.Key = strings.Repeat("x", 513) },
		func(u *core.AttentionUpdate) { u.Key = strings.Repeat("é", 257) },
		func(u *core.AttentionUpdate) { u.RunID = "" },
		func(u *core.AttentionUpdate) { u.StartedAt = time.Time{} },
		func(u *core.AttentionUpdate) { u.Phase = "unknown" },
		func(u *core.AttentionUpdate) { u.State = "resolved" },
		func(u *core.AttentionUpdate) { u.State = "recovering" },
		func(u *core.AttentionUpdate) { u.RecoveryTimeout = time.Hour },
		func(u *core.AttentionUpdate) { u.Phase, u.State, u.RecoveryTimeout = "finish", "recovering", time.Hour },
	} {
		u := base
		mutate(&u)
		result, err := r.ApplyAttentionUpdate(context.Background(), u)
		require.Error(t, err)
		require.False(t, result.Managed)
	}
	managed, err := r.AttentionManagedChats(context.Background(), "test-user", []string{base.ChatID})
	require.NoError(t, err)
	require.Empty(t, managed)
	require.Empty(t, attentionList(t, r))
}

func TestAttentionConcurrentActorsConverge(t *testing.T) {
	t.Parallel()
	r, cleanup := setupTestDB(t)
	defer cleanup()
	attentionChat(t, r, "concurrent")
	at := time.Now().UTC()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state := "action_required"
			if i == 11 {
				state = "resolved"
			}
			_, err := r.ApplyAttentionUpdate(context.Background(), core.AttentionUpdate{ChatID: "concurrent", RunID: fmt.Sprintf("run-%02d", i), StartedAt: at, Phase: "finish", Key: "key", State: state})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Empty(t, attentionList(t, r), "greatest immutable actor wins regardless of receipt order")
}
