// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Ledger retention (internal/db/ledger_retention.go): what compaction and the
// step prune delete, and — the point — that every reader of what is left
// reads exactly what it read before.

// chatReads is everything a reader takes from a chat's update log other than a
// replay: the snapshot's heads, each workflow's node heads, scroll-back's
// errors, the high-water mark, and the types compaction keeps whole.
type chatReads struct {
	Heads  []string
	Nodes  map[string][]string
	Errors []string
	Latest int64
	Whole  []string
}

func readChat(t *testing.T, env *storetest.Env, chatID string, workflows ...string) chatReads {
	t.Helper()
	ctx := context.Background()
	ids := func(us []core.ChatUpdate) []string {
		out := make([]string, len(us))
		for i, u := range us {
			out[i] = fmt.Sprintf("%d:%d:%s:%s", u.SequenceNumber, u.UpdateType, u.EntityID, u.Data)
		}
		return out
	}
	var r chatReads
	heads, err := store.LatestNonMessageUpdatesPerEntity(ctx, env.ORM, chatID)
	if err != nil {
		t.Fatal(err)
	}
	r.Heads = ids(heads)
	r.Nodes = map[string][]string{}
	for _, wf := range workflows {
		nodes, err := store.LatestWorkflowNodeUpdates(ctx, env.ORM, chatID, wf)
		if err != nil {
			t.Fatal(err)
		}
		r.Nodes[wf] = ids(nodes)
	}
	errs, err := store.ErrorUpdatesInRange(ctx, env.ORM, chatID, nil, "no-such-thread", 0)
	if err != nil {
		t.Fatal(err)
	}
	r.Errors = ids(errs)
	if r.Latest, err = store.LatestChatUpdateSequence(ctx, env.ORM, chatID); err != nil {
		t.Fatal(err)
	}
	all, err := store.ChatUpdatesSince(ctx, env.ORM, chatID, 0, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range all {
		switch u.UpdateType {
		case reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS,
			reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION:
			r.Whole = append(r.Whole, ids([]core.ChatUpdate{u})...)
		}
	}
	return r
}

// ageChatUpdates backdates a chat's updates at or below seq to at.
func ageChatUpdates(t *testing.T, env *storetest.Env, chatID string, seq int64, at time.Time) {
	t.Helper()
	mustExec(t, env, `UPDATE chat_updates SET created_at = $1 WHERE chat_id = $2 AND sequence_number <= $3`, at, chatID, seq)
}

func chatSeqs(t *testing.T, env *storetest.Env, chatID string) map[int64]reliantv1.ChatUpdateType {
	t.Helper()
	all, err := store.ChatUpdatesSince(context.Background(), env.ORM, chatID, 0, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]reliantv1.ChatUpdateType{}
	for _, u := range all {
		out[u.SequenceNumber] = u.UpdateType
	}
	return out
}

// TestCompactChatUpdates_ReadersReadWhatTheyReadBefore writes a chat's history
// of every kind of update, ages most of it, compacts, and holds every reader
// to what it read before — while the rows nothing reads are gone.
func TestCompactChatUpdates_ReadersReadWhatTheyReadBefore(t *testing.T) {
	env, ctx := storetest.New(t), context.Background()
	const chat = "chat-compact"
	seedChat(t, env, chat)
	write := func(typ reliantv1.ChatUpdateType, entity, data string) int64 {
		t.Helper()
		if err := env.Repo.CreateChatUpdate(ctx, chat, typ, entity, data); err != nil {
			t.Fatal(err)
		}
		seq, err := env.Repo.GetLatestUpdateSequence(ctx, chat)
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	var (
		msg      = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE
		tool     = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL
		final    = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED
		drained  = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED
		approval = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
		thread   = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD
		question = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION
		node     = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION
		status   = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS
		errType  = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR
		refetch  = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH
		chatType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_CHAT
		info     = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO
	)

	// The aged part of the history.
	oldMsg := write(msg, "m-1", `{"id":"m-1"}`)
	oldTool := write(tool, "tc-1", `{"status":"running"}`)
	write(tool, "tc-1", `{"status":"done"}`)
	oldFinal := write(final, "m-1", `{}`)
	oldDrain := write(drained, "drain-1", `{}`)
	apOld := write(approval, "ap-1", `{"status":"pending"}`)
	write(approval, "ap-1", `{"status":"approved"}`)            // ap-1's head
	apTwoOld := write(approval, "ap-2", `{"status":"pending"}`) // superseded by the young part
	write(thread, "wf-1", `{"thread":"th-a"}`)
	write(thread, "wf-1", `{"thread":"th-b"}`)
	write(question, "question-q-1-100", `{"status":"pending"}`)
	write(question, "question-q-1-200", `{"status":"resolved"}`)
	n1Old := write(node, "wf-1:n1", `{"s":"running"}`)
	write(node, "wf-1:n1", `{"s":"running","turn":2}`)
	n2Old := write(node, "wf-1:n2", `{"s":"done"}`) // superseded by the young part
	write(node, "wf-2:n1", `{"s":"done"}`)          // wf-2:n1's head
	write(status, "wf-1", `{"status":"running"}`)
	write(status, "wf-1", `{"status":"completed"}`)
	write(errType, "err-1", `{"attempt":1}`)
	write(errType, "err-1", `{"attempt":2}`)
	refetchOld := write(refetch, "workflow_executions", `{"n":1}`)
	write(refetch, "workflow_executions", `{"n":2}`)
	chatOld := write(chatType, chat, `{"title":"a"}`)
	write(chatType, chat, `{"title":"b"}`) // the chat entity's CHAT head
	write(info, chat, `{"i":1}`)           // same entity, another type: its own head
	aged := write(node, "wf-1:n1", `{"s":"done"}`)

	// The young part.
	write(approval, "ap-2", `{"status":"denied"}`)
	write(msg, "m-2", `{"id":"m-2"}`)
	write(node, "wf-1:n2", `{"s":"rerun"}`)

	now := time.Now()
	ageChatUpdates(t, env, chat, aged, now.Add(-48*time.Hour))
	before := readChat(t, env, chat, "wf-1", "wf-2")

	res, err := env.Repo.CompactChatUpdates(ctx, now.Add(-24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	after := readChat(t, env, chat, "wf-1", "wf-2")
	requireSame(t, "every reader", after, before)

	if res.Chats != 1 || res.Examined != aged {
		t.Fatalf("result = %+v, want 1 chat and %d aged updates examined", res, aged)
	}
	left := chatSeqs(t, env, chat)
	gone := []int64{oldMsg, oldTool, oldTool + 1, oldFinal, oldDrain, apOld, apTwoOld, n1Old, n1Old + 1, n2Old, refetchOld, chatOld}
	for _, gone := range gone {
		if typ, ok := left[gone]; ok {
			t.Errorf("update %d (%s) survived compaction; nothing reads it", gone, typ)
		}
	}
	if res.Deleted != int64(len(gone)) || len(left) != int(before.Latest)-len(gone) {
		t.Errorf("deleted %d updates (%d left of %d), want the %d no reader needs", res.Deleted, len(left), before.Latest, len(gone))
	}
	if through, err := env.Repo.GetChatCompactedThrough(ctx, chat); err != nil || through != aged {
		t.Fatalf("watermark = %d, %v; want %d, the newest aged update", through, err, aged)
	}

	// A second pass finds nothing to do: the chat's first update past its
	// watermark is young.
	res, err = env.Repo.CompactChatUpdates(ctx, now.Add(-24*time.Hour), 0)
	if err != nil || res != (db.ChatCompactionResult{}) {
		t.Fatalf("second pass = %+v, %v; want nothing", res, err)
	}
}

// TestCompactChatUpdates_BatchesAndIdleChats: a backlog longer than one batch
// is compacted in several, the watermark following; a chat whose newest update
// has aged keeps it (the high-water mark) and is never due again; a chat with
// nothing aged is not due at all.
func TestCompactChatUpdates_BatchesAndIdleChats(t *testing.T) {
	env, ctx := storetest.New(t), context.Background()
	const chat = "chat-backlog"
	seedChat(t, env, chat)
	seedChat(t, env, "chat-young")

	// 2,500 NODE_EXECUTION updates of one node, then a message: written in
	// one statement, with the counter row the allocator would have left.
	const n = 2500
	old := time.Now().Add(-72 * time.Hour)
	mustExec(t, env, `INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		SELECT $1 || '-' || s, $1, s, 9, 'wf-1:loop', '{"turn":' || s || '}', $2::timestamptz + s * interval '1 ms'
		  FROM generate_series(1, $3::int) s`, chat, old, n)
	mustExec(t, env, `INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		VALUES ($1, $2, $3, 1, 'm-1', '{}', $4)`, fmt.Sprintf("%s-%d", chat, n+1), chat, n+1, old.Add(time.Hour))
	mustExec(t, env, `INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq) VALUES ('chat', $1, $2)`, chat, n+1)
	if err := env.Repo.CreateChatUpdate(ctx, "chat-young", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, "m", `{}`); err != nil {
		t.Fatal(err)
	}
	before := readChat(t, env, chat, "wf-1")

	cutoff := time.Now().Add(-24 * time.Hour)
	due, err := store.ChatsDueForCompaction(ctx, env.ORM, cutoff, "", 10)
	if err != nil || len(due) != 1 || due[0].ChatID != chat {
		t.Fatalf("due = %+v, %v; want only %s (chat-young has nothing aged)", due, err, chat)
	}
	res, err := env.Repo.CompactChatUpdates(ctx, cutoff, 0)
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "every reader", readChat(t, env, chat, "wf-1"), before)
	// Every node update but the newest goes; the message is the chat's newest
	// update, which the high-water mark reads, so it stays.
	if res.Deleted != n-1 || res.Examined != n+1 {
		t.Fatalf("result = %+v, want %d deleted of %d examined", res, n-1, n+1)
	}
	if left := chatSeqs(t, env, chat); len(left) != 2 || left[n] == 0 || left[n+1] == 0 {
		t.Fatalf("left %v, want the node's head %d and the newest update %d", left, n, n+1)
	}
	if through, _ := env.Repo.GetChatCompactedThrough(ctx, chat); through != n+1 {
		t.Fatalf("watermark %d, want %d", through, n+1)
	}
	if due, err := store.ChatsDueForCompaction(ctx, env.ORM, cutoff, "", 10); err != nil || len(due) != 0 {
		t.Fatalf("due after compaction = %+v, %v; want none", due, err)
	}
	if through, _ := env.Repo.GetChatCompactedThrough(ctx, "chat-young"); through != 0 {
		t.Fatalf("chat-young's watermark %d, want 0", through)
	}
}

// stepRow is a step of a pruning fixture.
type stepRow struct {
	id, workflow, step, activity, output string
	loopNode                             string
	loopIteration                        int64
}

func seedRetentionSteps(t *testing.T, env *storetest.Env, rows []stepRow) {
	t.Helper()
	for i, s := range rows {
		exec := &core.StepExecution{
			ID: s.id, WorkflowID: s.workflow, StepID: s.step, ActivityName: s.activity,
			CreatedAt: l2Base.Add(time.Duration(i) * time.Second),
		}
		if s.output != "" {
			exec.OutputJSON = ptr(s.output)
		}
		if s.loopNode != "" {
			exec.LoopNodeID, exec.LoopIteration = ptr(s.loopNode), ptr(s.loopIteration)
		}
		if err := store.InsertStepExecution(context.Background(), env.ORM, exec); err != nil {
			t.Fatalf("step %s: %v", s.id, err)
		}
	}
}

// runSteps is a run's steps: a user-facing step and the "-save" row that
// renders it as a message, the internal steps of an agent turn (CallLLM,
// ExecuteTools and its own "-save"), and the same inside a loop iteration.
func runSteps(wf string) []stepRow {
	msg := func(id string) string { return `{"message_id":"` + id + `"}` }
	return []stepRow{
		{id: wf + "-run", workflow: wf, step: "run", activity: "ExecuteRunStep", output: `{"stdout":"ok"}`},
		{id: wf + "-run-save", workflow: wf, step: "run-save", activity: "SaveMessage", output: msg(wf + "-m1")},
		{id: wf + "-llm", workflow: wf, step: "call_llm", activity: "CallLLM", output: `{"text":"long reply"}`},
		{id: wf + "-tools", workflow: wf, step: "execute_tools", activity: "ExecuteTools", output: `{"results":[]}`},
		{id: wf + "-tools-save", workflow: wf, step: "execute_tools-save", activity: "SaveMessage", output: msg(wf + "-m2")},
		{id: wf + "-loop", workflow: wf, step: "body", activity: "Action", output: `{}`, loopNode: "loop", loopIteration: 1},
		{id: wf + "-loop-save", workflow: wf, step: "body-save", activity: "SaveMessage", output: msg(wf + "-m3"), loopNode: "loop", loopIteration: 1},
		{id: wf + "-loop-save-2", workflow: wf, step: "body-save", activity: "SaveMessage", output: msg(wf + "-m4"), loopNode: "loop", loopIteration: 2},
		{id: wf + "-status", workflow: wf, step: "status", activity: "WorkflowStatus"},
	}
}

// prunable is the ids of runSteps(wf) a prune deletes: its internal steps,
// except the "-save" rows the timeline renders (run-save, and loop-save of
// iteration 1; iteration 2 has no user-facing step to render it).
func prunable(wf string) []string {
	return []string{wf + "-llm", wf + "-tools", wf + "-tools-save", wf + "-loop-save-2", wf + "-status"}
}

func retainedStepIDs(t *testing.T, env *storetest.Env) map[string]bool {
	t.Helper()
	rows, err := env.SQL.Query(`SELECT id FROM step_executions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

// TestPruneStepExecutions_KeepsTheTimelineAndLiveRuns: a root run that
// completed or was cancelled before the cutoff loses its internal steps, and
// so do its finished children; the chat timeline (the BASIC read) is exactly
// what it was; and nothing of a running, paused, failed or recent run, or of a
// child still running under a finished root, is touched.
func TestPruneStepExecutions_KeepsTheTimelineAndLiveRuns(t *testing.T) {
	env, ctx := storetest.New(t), context.Background()
	seedChat(t, env, "chat-prune")
	old, recent := time.Now().Add(-60*24*time.Hour), time.Now().Add(-time.Hour)
	runs := []struct {
		id          string
		parent      *string
		status      core.WorkflowStatus
		completedAt *time.Time
		pruned      bool
	}{
		{"wf-done", nil, core.Completed(), &old, true},
		{"wf-done-child", ptr("wf-done"), core.Completed(), &old, true},
		{"wf-done-live-child", ptr("wf-done"), core.Active(), nil, false},
		{"wf-cancelled", nil, core.Cancelled(), &old, true},
		{"wf-failed", nil, core.Failed(), &old, false},
		{"wf-paused", nil, core.Paused(), nil, false},
		{"wf-active", nil, core.Active(), nil, false},
		{"wf-recent", nil, core.Completed(), &recent, false},
	}
	var all []stepRow
	for _, r := range runs {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow(r.id, "chat-prune", r.parent, r.status, l2Base)); err != nil {
			t.Fatal(err)
		}
		if r.completedAt != nil {
			mustExec(t, env, `UPDATE workflows SET completed_at = $1 WHERE id = $2`, *r.completedAt, r.id)
		}
		all = append(all, runSteps(r.id)...)
	}
	seedRetentionSteps(t, env, all)
	timeline := func() []string {
		steps, err := store.ListBasicChatStepExecutions(ctx, env.ORM, "chat-prune")
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(steps))
		for i, s := range steps {
			b, _ := json.Marshal(s)
			out[i] = string(b)
		}
		return out
	}
	before := timeline()

	res, err := env.Repo.PruneStepExecutions(ctx, time.Now().Add(-30*24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "the chat timeline", timeline(), before)

	left := retainedStepIDs(t, env)
	want := 0
	for _, r := range runs {
		gone := map[string]bool{}
		if r.pruned {
			for _, id := range prunable(r.id) {
				gone[id] = true
				want++
			}
		}
		for _, s := range runSteps(r.id) {
			if left[s.id] == gone[s.id] {
				t.Errorf("%s: step %s present=%v, want %v", r.id, s.id, left[s.id], !gone[s.id])
			}
		}
	}
	if res.Runs != 2 || res.Deleted != int64(want) {
		t.Fatalf("result = %+v, want 2 root runs and %d steps", res, want)
	}

	// The run's diagnostics degrade to what is left; they do not fail.
	if rows, err := store.ListRunNodeExecutions(ctx, env.ORM, "wf-done", 50); err != nil || len(rows) == 0 {
		t.Fatalf("diagnostics of a pruned run = %d rows, %v", len(rows), err)
	}
	// The cursor holds: a second pass re-reads none of it.
	if res, err := env.Repo.PruneStepExecutions(ctx, time.Now().Add(-30*24*time.Hour), 0); err != nil || res != (db.StepRetentionResult{}) {
		t.Fatalf("second pass = %+v, %v; want nothing", res, err)
	}
	if at, id, err := store.ReadLedgerRetentionCursor(ctx, env.ORM, "step_executions"); err != nil || id == "" || at.Sub(old).Abs() > time.Millisecond {
		t.Fatalf("cursor = %v %q, %v; want the last pruned root at %v", at, id, err, old)
	}
}
