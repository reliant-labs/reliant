// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Bug 94324f76 (designs/disk-audit-2026-11-12.md): a sub-agent's worktree was
// judged by its parent CHAT's runs. Orchestrator chats always have a run going,
// so every worktree any of their sub-agents ever used counted as running, with
// last_active = now, and was never archived; the machine's disk filled with
// ~380 of them. A thread-bound worktree is judged by that thread's own work.

// orchestrator is a chat bound to its own worktree with a run that never ends,
// and a sub-agent thread spawned into "sub", whose own run finished 3 days ago.
func orchestrator(t *testing.T, env *storetest.Env) *core.Chat {
	t.Helper()
	ctx := context.Background()
	chat := landedFixture(t, env, "orch")
	mustExecConv(t, env, `UPDATE chats SET last_active = NOW() WHERE id = $1`, chat.ID)
	mustExecConv(t, env, `INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, state, stop_reason)
		VALUES ('wf-orch', $1, 'wf', 'wf-orch', NOW() - interval '5 days', 2, 0)`, chat.ID)
	mustExecConv(t, env, `INSERT INTO provider_backoff (thread_id, chat_id, waiting_since, resume_at, reason, updated_at)
		VALUES ('wf-orch', $1, NOW() - interval '1 minute', NOW() + interval '1 hour', $2, NOW())`, chat.ID, core.ReasonUsageLimit)
	sub := &core.Worktree{ID: "sub", ProjectID: storetest.SeedProjectID, Name: "sub", Path: "/tmp/sub", Branch: "sub"}
	require.NoError(t, env.Repo.CreateWorktree(ctx, sub))
	mustExecConv(t, env, `UPDATE worktrees SET last_active = NOW() - interval '4 days', created_at = NOW() - interval '4 days' WHERE id = 'sub'`)
	mustExecConv(t, env, `INSERT INTO workflows (id, chat_id, parent_id, workflow_name, thread, created_at, completed_at, state, stop_reason)
		VALUES ('wf-sub', $1, 'wf-orch', 'wf', 't-sub', NOW() - interval '4 days', NOW() - interval '3 days', 3, 1)`, chat.ID)
	mustExecConv(t, env, `INSERT INTO threads (id, chat_id, parent_thread_id, workflow_id, created_at, origin, status, worktree_id)
		VALUES ('t-sub', $1, 'wf-orch', 'wf-sub', NOW() - interval '4 days', 'spawn', 3, 'sub')`, chat.ID)
	return chat
}

func TestWorktreeRunActivities_ParentChatRunsDoNotHoldASubAgentWorktree(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	chat := orchestrator(t, env)

	acts, err := store.WorktreeRunActivities(ctx, env.ORM, []string{"sub", *chat.WorktreeID})
	require.NoError(t, err)
	require.True(t, acts[*chat.WorktreeID].Running, "the chat's own workspace is held by its run")
	require.False(t, acts["sub"].Running, "the orchestrator's run and wait are not the sub-agent's")
	require.WithinDuration(t, time.Now().Add(-3*24*time.Hour), acts["sub"].LastActive, 2*time.Minute,
		"its last activity is its own run's stop, not the orchestrator's")

	running, err := env.Repo.ListWorktreeIDsWithRunningChat(ctx, []string{"sub", *chat.WorktreeID})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{*chat.WorktreeID: true}, running)

	// The end-to-end symptom: idle retirement now archives it, under the row
	// lock, while the orchestrator keeps running.
	ok, err := store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, "sub", time.Now().Add(-72*time.Hour))
	require.NoError(t, err)
	require.True(t, ok, "a sub-agent worktree quiet for 72h is retired while its parent chat runs")
	ok, err = store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, *chat.WorktreeID, time.Now().Add(-72*time.Hour))
	require.NoError(t, err)
	require.False(t, ok, "the running chat's own workspace is not")
}

func TestWorktreeRunActivities_ASubAgentsOwnWorkHoldsItsWorktree(t *testing.T) {
	for name, tc := range map[string]struct {
		setup   []string
		running bool
	}{
		// The sub-agent resumed (spawn with agent_id): a new run on its thread.
		"resumed": {[]string{`INSERT INTO workflows (id, chat_id, parent_id, workflow_name, thread, created_at, state, stop_reason)
			VALUES ('wf-sub-2', $1, 'wf-orch', 'wf', 't-sub', NOW(), 2, 0)`}, true},
		"paused": {[]string{`UPDATE workflows SET state = 3, stop_reason = 3 WHERE id = 'wf-sub'`}, true},
		// A run the sub-agent's run started (a workflow node, a nested agent).
		"descendant run": {[]string{`INSERT INTO workflows (id, chat_id, parent_id, workflow_name, thread, created_at, state, stop_reason)
			VALUES ('wf-nested', $1, 'wf-sub', 'wf', 't-nested', NOW(), 1, 0)`}, true},
		// A thread it spawned with no workspace of its own.
		"descendant thread": {[]string{`INSERT INTO threads (id, chat_id, parent_thread_id, created_at, origin, status)
			VALUES ('t-nested', $1, 't-sub', NOW() - interval '4 days', 'spawn', 2)`, `INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, state, stop_reason)
			VALUES ('wf-nested', $1, 'wf', 't-nested', NOW(), 2, 0)`}, true},
		"its usage-limit wait": {[]string{`INSERT INTO provider_backoff (thread_id, chat_id, waiting_since, resume_at, reason, updated_at)
			VALUES ('t-sub', $1, NOW() - interval '3 days', NOW() + interval '1 hour', 'usage_limit', NOW())`}, true},
		// A sibling sub-agent of the same chat, in another workspace.
		"a sibling's run": {[]string{`INSERT INTO threads (id, chat_id, parent_thread_id, created_at, origin, status)
			VALUES ('t-sib', $1, 'wf-orch', NOW() - interval '4 days', 'spawn', 2)`, `INSERT INTO workflows (id, chat_id, parent_id, workflow_name, thread, created_at, state, stop_reason)
			VALUES ('wf-sib', $1, 'wf-orch', 'wf', 't-sib', NOW(), 2, 0)`}, false},
	} {
		t.Run(name, func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := orchestrator(t, env)
			for _, q := range tc.setup {
				if strings.Contains(q, "$1") {
					mustExecConv(t, env, q, chat.ID)
				} else {
					mustExecConv(t, env, q)
				}
			}
			acts, err := store.WorktreeRunActivities(ctx, env.ORM, []string{"sub"})
			require.NoError(t, err)
			require.Equal(t, tc.running, acts["sub"].Running)
			ok, err := store.ArchiveRecoverableIdleWorktree(ctx, env.ORM, "sub", time.Now().Add(-72*time.Hour))
			require.NoError(t, err)
			require.Equal(t, !tc.running, ok, "archived only when nothing of its own runs")
		})
	}
}

// A worktree bound to the chat itself keeps the chat's whole scope: every run
// of a chat works in the chat's workspace.
func TestWorktreeRunActivities_AChatBoundWorktreeKeepsTheChatsScope(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	chat := landedFixture(t, env, "plain")
	mustExecConv(t, env, `INSERT INTO threads (id, chat_id, created_at, origin, status) VALUES ('t-any', $1, NOW(), 'spawn', 2)`, chat.ID)
	mustExecConv(t, env, `INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at, state, stop_reason)
		VALUES ('wf-any', $1, 'wf', 't-any', NOW(), 2, 0)`, chat.ID)
	acts, err := store.WorktreeRunActivities(ctx, env.ORM, []string{*chat.WorktreeID})
	require.NoError(t, err)
	require.True(t, acts[*chat.WorktreeID].Running, "any run of a chat bound to it")
}
