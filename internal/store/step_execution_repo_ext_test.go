// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

type stepSeed struct {
	id, workflow, step, activity, nodePath string
	output                                 string
	loopNode                               string
	loopIteration                          int64
}

// seedSteps writes the steps of two workflows of one chat, alternating
// layers, plus a third workflow in another chat that no chat read may return.
func seedSteps(t *testing.T, env *storetest.Env) {
	t.Helper()
	ctx := context.Background()
	seedChat(t, env, "chat-steps")
	seedChat(t, env, "chat-other")
	for _, w := range []*core.Workflow{
		newWorkflow("wf-s1", "chat-steps", nil, core.Active(), l2Base),
		newWorkflow("wf-s2", "chat-steps", ptr("wf-s1"), core.Active(), l2Base),
		newWorkflow("wf-other", "chat-other", nil, core.Active(), l2Base),
	} {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatal(err)
		}
	}
	seeds := []stepSeed{
		{id: "s1", workflow: "wf-s1", step: "agent", activity: "RunAgent", nodePath: "agent", output: `{"answer":1}`},
		{id: "s2", workflow: "wf-s1", step: "agent-save", activity: "SaveMessage", output: `{"message_id":"msg-1"}`},
		{id: "s3", workflow: "wf-s1", step: "call_llm", activity: "CallLLM", nodePath: "agent.agent_loop.call_llm", output: `{"tokens":3}`},
		{id: "s4", workflow: "wf-s2", step: "loop_body", activity: "Shell", nodePath: "loop.body", output: `{"exit":0}`, loopNode: "loop", loopIteration: 1},
		{id: "s5", workflow: "wf-s2", step: "loop_body-save", activity: "SaveMessage", output: `{"message_id":"msg-2"}`, loopNode: "loop", loopIteration: 1},
		{id: "s6", workflow: "wf-s2", step: "loop_body-save", activity: "SaveMessage", output: `{"message_id":"msg-3"}`, loopNode: "loop", loopIteration: 2},
		{id: "s7", workflow: "wf-s1", step: "agent", activity: "RunAgent", nodePath: "agent", output: `not json`},
		{id: "s8", workflow: "wf-other", step: "agent", activity: "RunAgent", nodePath: "agent", output: `{}`},
	}
	for i, s := range seeds {
		exec := &core.StepExecution{
			ID: s.id, WorkflowID: s.workflow, StepID: s.step, ActivityName: s.activity,
			OutputJSON: sql.NullString{String: s.output, Valid: true},
			ExitCode:   sql.NullInt64{Int64: int64(i), Valid: i%2 == 0},
			Success:    sql.NullBool{Bool: i%3 == 0, Valid: i != 4},
			DurationMs: sql.NullInt64{Int64: 10 * int64(i), Valid: true},
			CreatedAt:  l2Base.Add(time.Duration(i) * time.Second),
			InputJSON:  sql.NullString{String: fmt.Sprintf(`{"i":%d}`, i), Valid: i != 1},
			Attempt:    sql.NullInt32{Int32: 1, Valid: true},
		}
		if s.nodePath != "" {
			exec.NodePath = sql.NullString{String: s.nodePath, Valid: true}
		}
		if s.loopNode != "" {
			exec.LoopNodeID = sql.NullString{String: s.loopNode, Valid: true}
			exec.LoopIteration = sql.NullInt64{Int64: s.loopIteration, Valid: true}
		}
		if i == 3 {
			exec.ErrorMessage = sql.NullString{String: "boom", Valid: true}
		}
		var err error
		if i%2 == 0 {
			err = store.InsertStepExecution(ctx, env.ORM, exec)
		} else {
			err = env.Repo.CreateStepExecution(ctx, exec)
		}
		if err != nil {
			t.Fatalf("seed step %s: %v", s.id, err)
		}
	}
}

func stepIDs[T any](rows []T, id func(T) string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = id(r)
	}
	return out
}

// TestStepExecutionReadsAgreeWithSQLC: every step read of the port returns
// what its twin does, over rows both layers wrote.
func TestStepExecutionReadsAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedSteps(t, env)

	for _, id := range []string{"s1", "s2", "s4", "s7"} {
		port, err := store.LoadStepExecution(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		twin, err := env.Repo.GetStepExecution(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "step "+id, port, twin)
	}
	if s2, _ := store.LoadStepExecution(ctx, env.ORM, "s2"); s2.SavedMessageID.String != "msg-1" {
		t.Fatalf("saved_message_id = %+v, want the generated msg-1", s2.SavedMessageID)
	}
	_, portErr := store.LoadStepExecution(ctx, env.ORM, "missing")
	_, twinErr := env.Repo.GetStepExecution(ctx, "missing")
	if portErr == nil || twinErr == nil || portErr.Error() != twinErr.Error() {
		t.Fatalf("missing step: port %v, twin %v", portErr, twinErr)
	}

	full := func(s *core.StepExecution) string { return s.ID }
	byWorkflow, err := store.ListWorkflowStepExecutions(ctx, env.ORM, "wf-s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(stepIDs(byWorkflow, full)); got != "[s1 s2 s3 s7]" {
		t.Fatalf("by workflow: %s", got)
	}
	twinByWorkflow, _ := env.Repo.GetStepExecutionsByWorkflow(ctx, "wf-s1")
	requireSame(t, "by workflow", byWorkflow, twinByWorkflow)

	byStep, err := store.ListStepExecutionsByStep(ctx, env.ORM, "wf-s1", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(stepIDs(byStep, full)); got != "[s1 s7]" {
		t.Fatalf("by step: %s", got)
	}
	twinByStep, _ := env.Repo.GetStepExecutionsByStep(ctx, "wf-s1", "agent")
	requireSame(t, "by step", byStep, twinByStep)

	records := []struct {
		nodePath string
		limit    int
		want     string
	}{
		{"", 50, "[s7 s6 s5 s4 s3 s2 s1]"},
		{"", 2, "[s7 s6]"},
		{"agent", 50, "[s7 s3 s2 s1]"}, // the node, what ran inside it, and its pre-node_path save
		{"loop", 50, "[s4]"},
		{"loop.body", 50, "[s4]"},
	}
	for _, r := range records {
		port, err := store.ListStepExecutionRecordsForChat(ctx, env.ORM, "chat-steps", r.nodePath, r.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(stepIDs(port, full)); got != r.want {
			t.Fatalf("records(%q, %d) = %s, want %s", r.nodePath, r.limit, got, r.want)
		}
		twin, _ := env.Repo.ListStepExecutionRecordsForChat(ctx, "chat-steps", r.nodePath, r.limit)
		requireSame(t, "records "+r.nodePath, port, twin)
	}

	chatID := func(s *core.ChatStepExecution) string { return s.ID }
	fullView, err := store.ListChatStepExecutions(ctx, env.ORM, "chat-steps", model.InternalActivities)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(stepIDs(fullView, chatID)); got != "[s1 s2 s3 s7 s4 s5 s6]" {
		t.Fatalf("full view: %s", got)
	}
	for _, s := range fullView {
		internal := s.ActivityName == "SaveMessage" || s.ActivityName == "CallLLM"
		if internal != (s.OutputJSON == "") {
			t.Fatalf("full view %s (%s): output %q, want it withheld exactly for internal activities", s.ID, s.ActivityName, s.OutputJSON)
		}
	}
	twinFull, _ := env.Repo.GetStepExecutionsForChat(ctx, "chat-steps")
	requireSame(t, "full view", fullView, twinFull)

	basic, err := store.ListBasicChatStepExecutions(ctx, env.ORM, "chat-steps")
	if err != nil {
		t.Fatal(err)
	}
	// The user-facing steps, each followed by the save of its own loop scope.
	if got := fmt.Sprint(stepIDs(basic, chatID)); got != "[s1 s2 s2 s7 s4 s5]" {
		t.Fatalf("basic view: %s", got)
	}
	twinBasic, _ := env.Repo.GetBasicStepExecutionsForChat(ctx, "chat-steps")
	requireSame(t, "basic view", basic, twinBasic)

	if err := store.RemoveWorkflowStepExecutions(ctx, env.ORM, "wf-s1"); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.DeleteStepExecutionsByWorkflow(ctx, "wf-s2"); err != nil {
		t.Fatal(err)
	}
	left, _ := store.ListStepExecutionRecordsForChat(ctx, env.ORM, "chat-steps", "", 50)
	if len(left) != 0 {
		t.Fatalf("deletes left %d steps of the chat", len(left))
	}
	if other, _ := store.ListWorkflowStepExecutions(ctx, env.ORM, "wf-other"); len(other) != 1 {
		t.Fatal("a delete reached another workflow's steps")
	}
}

// literalNotInList matches `activity_name NOT IN ( 'A', 'B', ... )`.
var literalNotInList = regexp.MustCompile(`(?s)activity_name\s+NOT\s+IN\s*\(([^)]*)\)`)

// TestUserFacingStepIndexPredicateMatchesInternalActivities: the BASIC step
// query repeats the internal-activity list as a literal, as does the partial
// index it relies on, because a partial index is only usable when the
// planner can prove its predicate from the query text. The two literals and
// workflowmodel.InternalActivities are one set in three places; if they
// drift the failure is a silent seq scan (or a missing indicator), not an
// error, so this pins them.
func TestUserFacingStepIndexPredicateMatchesInternalActivities(t *testing.T) {
	want := append([]string(nil), model.InternalActivities...)
	sort.Strings(want)

	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	split := func(list string) []string {
		var names []string
		for _, part := range strings.Split(list, ",") {
			part = strings.TrimSuffix(strings.TrimSpace(part), "::text")
			names = append(names, strings.Trim(part, "'"))
		}
		sort.Strings(names)
		return names
	}

	query := literalNotInList.FindAllStringSubmatch(read("step_execution_repo_ext.go"), -1)
	if len(query) < 2 {
		t.Fatalf("expected the BASIC query's two literal NOT IN lists in step_execution_repo_ext.go, found %d", len(query))
	}
	for _, m := range query {
		requireSame(t, "BASIC step query literal", split(m[1]), want)
	}

	// The index is in the baseline, which is pg_dump output: it renders the
	// predicate as <> ALL (ARRAY[...]).
	baselines, err := filepath.Glob("../../db/migrations/*_baseline.up.sql")
	if err != nil || len(baselines) != 1 {
		t.Fatalf("expected exactly one baseline in db/migrations: %v, %v", baselines, err)
	}
	m := regexp.MustCompile(`idx_step_executions_user_facing ON [^;]*?ARRAY\[([^\]]*)\]`).FindStringSubmatch(read(baselines[0]))
	if m == nil {
		t.Fatalf("idx_step_executions_user_facing missing from %s", baselines[0])
	}
	requireSame(t, "index predicate", split(m[1]), want)
}
