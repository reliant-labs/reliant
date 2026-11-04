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

// Run diagnostics is scoped to the workflow tree, not every root of a chat,
// and never projects resolved inputs or arbitrary output fields.
func TestListRunNodeExecutions(t *testing.T) {
	env := storetest.New(t)
	seedSteps(t, env)
	rows, err := store.ListRunNodeExecutions(context.Background(), env.ORM, "wf-s1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4 graph attempts", len(rows))
	}
	for _, row := range rows {
		if row.WorkflowID == "wf-other" || row.InputJSON.Valid || strings.Contains(row.OutputJSON.String, "tokens") || strings.Contains(row.OutputJSON.String, "answer") {
			t.Fatalf("unwanted data: %+v", row)
		}
	}
	rows, err = store.ListRunNodeExecutions(context.Background(), env.ORM, "wf-s2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].WorkflowID != "wf-s2" {
		t.Fatalf("subtree and limit: %+v", rows)
	}
}

func TestRunDiagnosticsBoundsBeforeJSONAndPreservesSkip(t *testing.T) {
	env := storetest.New(t)
	seedSteps(t, env)
	ctx := context.Background()
	// A huge JSON string and huge malformed JSON must both be omitted before
	// any JSON cast, never transferred whole to the API process.
	for i, raw := range []string{`{"stdout":"` + strings.Repeat("secret", 400000) + `"}`, strings.Repeat("bad json", 300000), `{"skipped":true}`} {
		_, err := env.SQL.ExecContext(ctx, `INSERT INTO step_executions(id,workflow_id,step_id,activity_name,output_json,created_at) VALUES($1,'wf-s1',$1,'CustomAction',$2,now())`, fmt.Sprintf("bounded-%d", i), raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListRunNodeExecutions(ctx, env.ORM, "wf-s1", 20)
	if err != nil {
		t.Fatal(err)
	}
	foundSkip := false
	for _, row := range rows {
		if len(row.OutputJSON.String) > 16384 {
			t.Fatalf("unbounded fetch: %d", len(row.OutputJSON.String))
		}
		if row.ID == "bounded-2" {
			foundSkip = strings.Contains(row.OutputJSON.String, `"skipped" : true`)
		}
		if row.ID == "bounded-0" || row.ID == "bounded-1" {
			if row.OutputJSON.String != `{"_diagnostic_omitted":true}` {
				t.Fatalf("unsafe output: %s", row.OutputJSON.String)
			}
		}
	}
	if !foundSkip {
		t.Fatal("explicit skipped flag lost in SQL projection")
	}
	// Parent pointers alone must not grant access to another chat's records.
	_, err = env.SQL.ExecContext(ctx, `UPDATE workflows SET parent_id='wf-s1' WHERE id='wf-other'`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListRunNodeExecutions(ctx, env.ORM, "wf-s1", 201)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.WorkflowID == "wf-other" {
			t.Fatal("cross-chat child leaked")
		}
	}
	// Budget is cumulative as well as per-row. Empty errors stay empty,
	// otherwise an omitted successful attempt could read as a failure.
	for i := 0; i < 20; i++ {
		_, err = env.SQL.ExecContext(ctx, `INSERT INTO step_executions(id,workflow_id,step_id,activity_name,output_json,created_at) VALUES($1,'wf-s1',$1,'ExecuteRunStep',$2,now())`, fmt.Sprintf("budget-%d", i), `{"stdout":"`+strings.Repeat("x", 14000)+`"}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err = store.ListRunNodeExecutions(ctx, env.ORM, "wf-s1", 201)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	omitted := 0
	for _, row := range rows {
		total += len(row.OutputJSON.String) + len(row.ErrorMessage.String)
		if strings.Contains(row.OutputJSON.String, "_diagnostic_omitted") {
			omitted++
		}
	}
	if total > 140000 || omitted == 0 {
		t.Fatalf("fetch budget not enforced: bytes=%d omitted=%d", total, omitted)
	}
}

type stepSeed struct {
	id, workflow, step, activity, nodePath string
	output                                 string
	loopNode                               string
	loopIteration                          int64
}

// seedSteps writes the steps of two workflows of one chat, plus a third
// workflow in another chat that no chat read may return. Step i (0-based, in
// the order below) is created i seconds after l2Base, takes i*10 ms, exits
// with code i when i is even (NULL otherwise), succeeds when i%3 == 0 (NULL
// for i == 4), has input {"i":i} (NULL for i == 1), and attempt 1.
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
		if err := store.InsertStepExecution(ctx, env.ORM, exec); err != nil {
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

func nstr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func nint(i int64) sql.NullInt64   { return sql.NullInt64{Int64: i, Valid: true} }
func nbool(b bool) sql.NullBool    { return sql.NullBool{Bool: b, Valid: true} }

func TestLoadStepExecution(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedSteps(t, env)
	at := func(i int) time.Time { return l2Base.Add(time.Duration(i) * time.Second) }

	want := map[string]*core.StepExecution{
		// A plain step: every nullable column set.
		"s1": {ID: "s1", WorkflowID: "wf-s1", StepID: "agent", ActivityName: "RunAgent", OutputJSON: nstr(`{"answer":1}`),
			ExitCode: nint(0), Success: nbool(true), DurationMs: nint(0), CreatedAt: at(0),
			InputJSON: nstr(`{"i":0}`), Attempt: sql.NullInt32{Int32: 1, Valid: true}, NodePath: nstr("agent")},
		// A save step: saved_message_id comes from the output, not from the writer; no exit code,
		// failed, no input and no node path.
		"s2": {ID: "s2", WorkflowID: "wf-s1", StepID: "agent-save", ActivityName: "SaveMessage", OutputJSON: nstr(`{"message_id":"msg-1"}`),
			Success: nbool(false), DurationMs: nint(10), CreatedAt: at(1), SavedMessageID: nstr("msg-1"),
			Attempt: sql.NullInt32{Int32: 1, Valid: true}},
		// A failed loop step: error message, loop scope, and no exit code.
		"s4": {ID: "s4", WorkflowID: "wf-s2", StepID: "loop_body", ActivityName: "Shell", OutputJSON: nstr(`{"exit":0}`),
			Success: nbool(true), DurationMs: nint(30), CreatedAt: at(3), LoopNodeID: nstr("loop"), LoopIteration: nint(1),
			InputJSON: nstr(`{"i":3}`), ErrorMessage: nstr("boom"), Attempt: sql.NullInt32{Int32: 1, Valid: true}, NodePath: nstr("loop.body")},
		// Success unknown (NULL), and a loop save.
		"s5": {ID: "s5", WorkflowID: "wf-s2", StepID: "loop_body-save", ActivityName: "SaveMessage", OutputJSON: nstr(`{"message_id":"msg-2"}`),
			ExitCode: nint(4), DurationMs: nint(40), CreatedAt: at(4), LoopNodeID: nstr("loop"), LoopIteration: nint(1),
			SavedMessageID: nstr("msg-2"), InputJSON: nstr(`{"i":4}`), Attempt: sql.NullInt32{Int32: 1, Valid: true}},
		// Output that is not JSON is stored as given, and saves nothing.
		"s7": {ID: "s7", WorkflowID: "wf-s1", StepID: "agent", ActivityName: "RunAgent", OutputJSON: nstr("not json"),
			ExitCode: nint(6), Success: nbool(true), DurationMs: nint(60), CreatedAt: at(6),
			InputJSON: nstr(`{"i":6}`), Attempt: sql.NullInt32{Int32: 1, Valid: true}, NodePath: nstr("agent")},
	}
	for id, w := range want {
		got, err := store.LoadStepExecution(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("LoadStepExecution(%s): %v", id, err)
		}
		requireSame(t, "step "+id, got, w)
	}
	if _, err := store.LoadStepExecution(ctx, env.ORM, "missing"); err == nil || err.Error() != "step execution not found: missing" {
		t.Fatalf("LoadStepExecution(missing) = %v, want \"step execution not found: missing\"", err)
	}

	// saved_message_id is generated: whatever the writer sets is ignored.
	forged := &core.StepExecution{ID: "forged", WorkflowID: "wf-s1", StepID: "x-save", ActivityName: "SaveMessage",
		OutputJSON: nstr(`{"message_id":"real"}`), SavedMessageID: nstr("forged"), CreatedAt: at(20)}
	if err := store.InsertStepExecution(ctx, env.ORM, forged); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.LoadStepExecution(ctx, env.ORM, "forged"); got.SavedMessageID != nstr("real") {
		t.Fatalf("saved_message_id = %+v, want the one derived from the output", got.SavedMessageID)
	}
	// A "-save" step whose output has no message id, or is not an object, saves nothing.
	for id, out := range map[string]string{"nomsg": `{"other":1}`, "arr": `[1]`, "text": `plain`} {
		if err := store.InsertStepExecution(ctx, env.ORM, &core.StepExecution{ID: id, WorkflowID: "wf-s1", StepID: "y-save", ActivityName: "SaveMessage",
			OutputJSON: nstr(out), CreatedAt: at(21)}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		if got, _ := store.LoadStepExecution(ctx, env.ORM, id); got.SavedMessageID.Valid {
			t.Fatalf("%s saved %+v, want no message", id, got.SavedMessageID)
		}
	}
}

func TestStepExecutionLists(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedSteps(t, env)
	full := func(s *core.StepExecution) string { return s.ID }

	for _, c := range []struct {
		name string
		wf   string
		want string
	}{
		{"every step of a workflow, oldest first", "wf-s1", "[s1 s2 s3 s7]"},
		{"a second workflow", "wf-s2", "[s4 s5 s6]"},
		{"a workflow with no steps", "wf-none", "[]"},
	} {
		got, err := store.ListWorkflowStepExecutions(ctx, env.ORM, c.wf)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if g := fmt.Sprint(stepIDs(got, full)); g != c.want {
			t.Fatalf("%s: listed %s, want %s", c.name, g, c.want)
		}
	}
	// The list carries the whole row, not just the key.
	byStep, _ := store.ListWorkflowStepExecutions(ctx, env.ORM, "wf-s1")
	if byStep[3].OutputJSON != nstr("not json") || byStep[3].InputJSON != nstr(`{"i":6}`) || byStep[0].NodePath != nstr("agent") {
		t.Fatalf("listed rows lost columns: %+v", byStep)
	}
}

func TestStepExecutionRecordsForChat(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedSteps(t, env)
	full := func(s *core.StepExecution) string { return s.ID }

	records := []struct {
		name     string
		chat     string
		nodePath string
		limit    int
		want     string
	}{
		{"whole chat, newest first", "chat-steps", "", 50, "[s7 s6 s5 s4 s3 s2 s1]"},
		{"limit cuts the oldest", "chat-steps", "", 2, "[s7 s6]"},
		{"limit of one", "chat-steps", "", 1, "[s7]"},
		{"limit zero", "chat-steps", "", 0, "[]"},
		{"limit equal to the rows", "chat-steps", "", 7, "[s7 s6 s5 s4 s3 s2 s1]"},
		// The node, what ran inside it, and (for rows written before node_path
		// existed) its step id and its "-save".
		{"a node and everything inside it", "chat-steps", "agent", 50, "[s7 s3 s2 s1]"},
		{"a node, limited", "chat-steps", "agent", 2, "[s7 s3]"},
		{"a loop node: only its inner paths", "chat-steps", "loop", 50, "[s4]"},
		{"an inner node", "chat-steps", "loop.body", 50, "[s4]"},
		{"a nested node", "chat-steps", "agent.agent_loop", 50, "[s3]"},
		{"a node nothing ran in", "chat-steps", "nowhere", 50, "[]"},
		{"a prefix of a node is not the node", "chat-steps", "agen", 50, "[]"},
		{"another chat", "chat-other", "", 50, "[s8]"},
		{"a chat with no steps", "chat-none", "", 50, "[]"},
	}
	for _, r := range records {
		got, err := store.ListStepExecutionRecordsForChat(ctx, env.ORM, r.chat, r.nodePath, r.limit)
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if g := fmt.Sprint(stepIDs(got, full)); g != r.want {
			t.Fatalf("%s: records(%q, %q, %d) = %s, want %s", r.name, r.chat, r.nodePath, r.limit, g, r.want)
		}
	}
	// A record carries the output that the chat views withhold for internal activities.
	got, _ := store.ListStepExecutionRecordsForChat(ctx, env.ORM, "chat-steps", "agent.agent_loop.call_llm", 1)
	if len(got) != 1 || got[0].ID != "s3" || got[0].OutputJSON != nstr(`{"tokens":3}`) || got[0].InputJSON != nstr(`{"i":2}`) {
		t.Fatalf("record of the LLM call = %+v, want its output and input", got)
	}

	// node ids are full of underscores, which LIKE would read as wildcards; and
	// rows created at the same instant order by id, newest first.
	seedChat(t, env, "chat-wild")
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("wf-wild", "chat-wild", nil, core.Active(), l2Base)); err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct{ id, path string }{{"w1", "a_b"}, {"w2", "a_b.c"}, {"w3", "aXb"}, {"w4", "aXb.c"}} {
		if err := store.InsertStepExecution(ctx, env.ORM, &core.StepExecution{ID: w.id, WorkflowID: "wf-wild", StepID: w.id, ActivityName: "Shell",
			NodePath: nstr(w.path), CreatedAt: l2Base}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListStepExecutionRecordsForChat(ctx, env.ORM, "chat-wild", "a_b", 50)
	if err != nil {
		t.Fatal(err)
	}
	if g := fmt.Sprint(stepIDs(got, full)); g != "[w2 w1]" {
		t.Fatalf("records of a_b = %s, want [w2 w1] (aXb must not match, ties newest id first)", g)
	}
}

func TestChatStepExecutionViews(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedSteps(t, env)
	at := func(i int) time.Time { return l2Base.Add(time.Duration(i) * time.Second) }
	chatStep := func(id, wf, step, activity string, i int, exit sql.NullInt64, success sql.NullBool, loop string, iter int64, saved, output string) *core.ChatStepExecution {
		c := &core.ChatStepExecution{ID: id, WorkflowID: wf, StepID: step, ActivityName: activity, ExitCode: exit, Success: success,
			DurationMs: nint(10 * int64(i)), CreatedAt: at(i), SavedMessageID: nstr(saved), OutputJSON: output}
		if loop != "" {
			c.LoopNodeID, c.LoopIteration = nstr(loop), nint(iter)
		}
		return c
	}
	none := sql.NullInt64{}
	unknown := sql.NullBool{}

	// The FULL view: every step of both workflows of the chat, by workflow and
	// time, with the output only for user-facing activities. Empty means withheld.
	wantFull := []*core.ChatStepExecution{
		chatStep("s1", "wf-s1", "agent", "RunAgent", 0, nint(0), nbool(true), "", 0, "", `{"answer":1}`),
		chatStep("s2", "wf-s1", "agent-save", "SaveMessage", 1, none, nbool(false), "", 0, "msg-1", ""),
		chatStep("s3", "wf-s1", "call_llm", "CallLLM", 2, nint(2), nbool(false), "", 0, "", ""),
		chatStep("s7", "wf-s1", "agent", "RunAgent", 6, nint(6), nbool(true), "", 0, "", "not json"),
		chatStep("s4", "wf-s2", "loop_body", "Shell", 3, none, nbool(true), "loop", 1, "", `{"exit":0}`),
		chatStep("s5", "wf-s2", "loop_body-save", "SaveMessage", 4, nint(4), unknown, "loop", 1, "msg-2", ""),
		chatStep("s6", "wf-s2", "loop_body-save", "SaveMessage", 5, none, nbool(false), "loop", 2, "msg-3", ""),
	}
	gotFull, err := store.ListChatStepExecutions(ctx, env.ORM, "chat-steps", model.InternalActivities)
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "full view", gotFull, wantFull)

	// With no internal activities named, every output is shipped; a nil list is the same.
	for _, internal := range [][]string{nil, {}} {
		all, err := store.ListChatStepExecutions(ctx, env.ORM, "chat-steps", internal)
		if err != nil {
			t.Fatal(err)
		}
		var outputs []string
		for _, s := range all {
			outputs = append(outputs, s.OutputJSON)
		}
		if want := `[{"answer":1} {"message_id":"msg-1"} {"tokens":3} not json {"exit":0} {"message_id":"msg-2"} {"message_id":"msg-3"}]`; fmt.Sprint(outputs) != want {
			t.Fatalf("outputs with internal=%#v: %v, want %s", internal, outputs, want)
		}
	}
	// A narrower list withholds only what it names.
	some, _ := store.ListChatStepExecutions(ctx, env.ORM, "chat-steps", []string{"RunAgent"})
	if some[0].OutputJSON != "" || some[3].OutputJSON != "" || some[4].OutputJSON != `{"exit":0}` || some[2].OutputJSON != `{"tokens":3}` {
		t.Fatalf("withholding RunAgent only: %q %q %q %q", some[0].OutputJSON, some[2].OutputJSON, some[3].OutputJSON, some[4].OutputJSON)
	}

	// The BASIC view: the user-facing steps, each followed by the save of its
	// own loop scope. s2 is listed once per "agent" attempt (s1 and s7), and
	// s6, iteration 2's save, belongs to no visible step. Saves carry no output.
	wantBasic := []*core.ChatStepExecution{
		wantFull[0],
		chatStep("s2", "wf-s1", "agent-save", "SaveMessage", 1, none, nbool(false), "", 0, "msg-1", ""),
		chatStep("s2", "wf-s1", "agent-save", "SaveMessage", 1, none, nbool(false), "", 0, "msg-1", ""),
		wantFull[3],
		wantFull[4],
		chatStep("s5", "wf-s2", "loop_body-save", "SaveMessage", 4, nint(4), unknown, "loop", 1, "msg-2", ""),
	}
	gotBasic, err := store.ListBasicChatStepExecutions(ctx, env.ORM, "chat-steps")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "basic view", gotBasic, wantBasic)

	// A chat with no steps, and the other chat's one step.
	for _, chat := range []string{"chat-none", "chat-other"} {
		full, err := store.ListChatStepExecutions(ctx, env.ORM, chat, model.InternalActivities)
		if err != nil {
			t.Fatal(err)
		}
		basic, err := store.ListBasicChatStepExecutions(ctx, env.ORM, chat)
		if err != nil {
			t.Fatal(err)
		}
		wantN := 0
		if chat == "chat-other" {
			wantN = 1
		}
		if len(full) != wantN || len(basic) != wantN {
			t.Fatalf("%s: %d full, %d basic steps, want %d each", chat, len(full), len(basic), wantN)
		}
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

// A save_message NODE is a run node (only "<node>-save" companions are not),
// and its text is projected; MarkDeclaredFailureNode marks the newest attempt
// of the top-level node once, never a row that already has an error, and
// reports false for a node with no attempt.
func TestRunDiagnosticsSaveMessageNodeAndDeclaredFailure(t *testing.T) {
	env := storetest.New(t)
	seedSteps(t, env)
	ctx := context.Background()
	for i, id := range []string{"stopped-1", "stopped-2"} {
		_, err := env.SQL.ExecContext(ctx, `INSERT INTO step_executions(id,workflow_id,step_id,activity_name,output_json,success,created_at)
VALUES($1,'wf-s1','stopped','SaveMessage',$2,1,now() + $3 * interval '1 second')`, id, `{"message":{"text":"why `+id+`"},"message_id":"m"}`, i)
		if err != nil {
			t.Fatal(err)
		}
	}
	marked, err := store.MarkDeclaredFailureNode(ctx, env.ORM, "wf-s1", "stopped")
	if err != nil || !marked {
		t.Fatalf("mark: %v %v", marked, err)
	}
	if again, err := store.MarkDeclaredFailureNode(ctx, env.ORM, "wf-s1", "stopped"); err != nil || again {
		t.Fatalf("re-mark must be a no-op: %v %v", again, err)
	}
	if none, err := store.MarkDeclaredFailureNode(ctx, env.ORM, "wf-s1", "verdict"); err != nil || none {
		t.Fatalf("no attempt: %v %v", none, err)
	}
	rows, err := store.ListRunNodeExecutions(ctx, env.ORM, "wf-s1", 20)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*core.StepExecution{}
	for _, row := range rows {
		byID[row.ID] = row
		if strings.HasSuffix(row.StepID, "-save") {
			t.Fatalf("companion row listed: %+v", row)
		}
	}
	newest, older := byID["stopped-2"], byID["stopped-1"]
	if newest == nil || older == nil {
		t.Fatalf("save_message node rows missing: %v", rows)
	}
	if newest.Success.Bool || newest.ErrorMessage.String != store.DeclaredFailureError || !strings.Contains(newest.OutputJSON.String, "why stopped-2") {
		t.Fatalf("newest attempt: %+v", newest)
	}
	if !older.Success.Bool || older.ErrorMessage.Valid || strings.Contains(older.OutputJSON.String, "message_id") {
		t.Fatalf("older attempt must be untouched and keep only the projection: %+v", older)
	}
}
