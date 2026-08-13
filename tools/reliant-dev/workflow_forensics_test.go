// Copyright (c) 2025 Reliant Labs
package main

import (
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// The counting rule this whole command rests on: a tool CALL is a TOOL_CALL
// block. TOOL_RESULT blocks carry the same tool_name, so anything that counts
// both reports roughly 2x for every tool that returned a result — and exactly
// 1x for tools whose result is never recorded (spawn, ask_user). That mix is
// what makes a wrong histogram look plausible, which is why it gets a test that
// asserts the failure mode by name rather than just the happy path.
func TestCountableToolCallExcludesToolResults(t *testing.T) {
	call := &db.MessageContentBlock{
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:  strPtr("view"),
	}
	result := &db.MessageContentBlock{
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolName:  strPtr("view"), // the result carries the SAME tool_name
	}

	if name, ok := countableToolCall(call); !ok || name != "view" {
		t.Fatalf("TOOL_CALL should count: got (%q, %v)", name, ok)
	}
	if _, ok := countableToolCall(result); ok {
		t.Fatal("TOOL_RESULT must NOT count — it is the result of a call already counted, and counting it doubles every tool that returned")
	}

	// Text and thinking blocks never count, even if a tool_name leaked in.
	for _, bt := range []reliantv1.ContentBlockType{
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING,
		reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE,
	} {
		b := &db.MessageContentBlock{BlockType: bt, ToolName: strPtr("view")}
		if _, ok := countableToolCall(b); ok {
			t.Fatalf("block_type %v must not count as a tool call", bt)
		}
	}

	// A call with no usable name is not a call.
	for _, b := range []*db.MessageContentBlock{
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL},
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolName: strPtr("")},
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, ToolName: strPtr("   ")},
		nil,
	} {
		if _, ok := countableToolCall(b); ok {
			t.Fatalf("nameless block must not count: %+v", b)
		}
	}
}

func TestForensicsToolPath(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"view file_path", `{"file_path":"internal/app.go","limit":200}`, "internal/app.go"},
		{"edit file_path", `{"file_path":"/abs/x.tsx","old_string":"a"}`, "/abs/x.tsx"},
		{"find_replace file_glob", `{"file_glob":"src/globals.css","find_pattern":"#fff"}`, "src/globals.css"},
		{"plain path key", `{"path":"docs/x.md"}`, "docs/x.md"},
		// Unrecognised shapes yield "" so callers report "no path" instead of
		// inventing one — a wrong path silently merges two files in the repeat
		// counts.
		{"no path field", `{"command":"go build ./..."}`, ""},
		{"not json", `not json at all`, ""},
		{"empty", ``, ""},
		{"blank value", `{"file_path":"   "}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var in *string
			if tc.input != "" {
				in = strPtr(tc.input)
			}
			if got := forensicsToolPath(in); got != tc.want {
				t.Fatalf("forensicsToolPath(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	if got := forensicsToolPath(nil); got != "" {
		t.Fatalf("nil tool_input = %q, want empty", got)
	}
}

// An absolute and a relative reference to one file must compare equal, or a
// repeated read is hidden by the form the agent happened to use.
func TestForensicsNormalizePath(t *testing.T) {
	root := "/tmp/scratchpad/dogfood/shopdemo"
	if got := forensicsNormalizePath(root+"/internal/app.go", root); got != "internal/app.go" {
		t.Fatalf("absolute under root = %q, want stripped", got)
	}
	// Trailing slash on the root is the same root.
	if got := forensicsNormalizePath(root+"/internal/app.go", root+"/"); got != "internal/app.go" {
		t.Fatalf("trailing-slash root = %q, want stripped", got)
	}
	// Already relative: unchanged, and equal to the stripped absolute form.
	if got := forensicsNormalizePath("internal/app.go", root); got != "internal/app.go" {
		t.Fatalf("relative = %q, want unchanged", got)
	}
	// A path outside the root must NOT be mangled into a false match.
	if got := forensicsNormalizePath("/other/place/app.go", root); got != "/other/place/app.go" {
		t.Fatalf("outside root = %q, want unchanged", got)
	}
	// A root that is a string prefix but not a path prefix must not strip.
	if got := forensicsNormalizePath(root+"-other/app.go", root); got != root+"-other/app.go" {
		t.Fatalf("sibling dir = %q, want unchanged", got)
	}
	// Without a root, nothing is guessed.
	if got := forensicsNormalizePath(root+"/internal/app.go", ""); got != root+"/internal/app.go" {
		t.Fatalf("no root = %q, want verbatim", got)
	}
}

func TestForensicsDisplayTool(t *testing.T) {
	cases := map[string]string{
		"mcp__chrome-devtools__take_screenshot": "take_screenshot",
		"mcp__reliant__bash":                    "bash",
		"view":                                  "view",
		"mcp__weird":                            "weird",
	}
	for in, want := range cases {
		if got := forensicsDisplayTool(in); got != want {
			t.Fatalf("forensicsDisplayTool(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForensicsSkillAndSpawnMeta(t *testing.T) {
	skill, action := forensicsSkillTarget(strPtr(`{"action":"load","path":"frontend-api-brief"}`))
	if skill != "frontend-api-brief" || action != "load" {
		t.Fatalf("skill target = (%q, %q)", skill, action)
	}
	if s, _ := forensicsSkillTarget(strPtr(`{"action":"list"}`)); s != "" {
		t.Fatalf("skill with no target = %q, want empty", s)
	}
	if s, _ := forensicsSkillTarget(nil); s != "" {
		t.Fatalf("nil skill input = %q", s)
	}

	title, preset := forensicsSpawnMeta(strPtr(`{"preset":"forge_implementer","title":"frontend-orders"}`))
	if title != "frontend-orders" || preset != "forge_implementer" {
		t.Fatalf("spawn meta = (%q, %q)", title, preset)
	}
}

// forensicsCost must not render absent data as $0.00 — a run whose cost column
// was never populated and a genuinely free run are opposite findings.
func TestForensicsCostDistinguishesUnrecordedFromZero(t *testing.T) {
	if got := forensicsCost(0, false); got != "n/a" {
		t.Fatalf("unrecorded cost = %q, want n/a — $0.00 would read as a measured free run", got)
	}
	if got := forensicsCost(0, true); got != "$0.00" {
		t.Fatalf("recorded zero cost = %q, want $0.00", got)
	}
	if got := forensicsCost(1.5, true); got != "$1.50" {
		t.Fatalf("recorded cost = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Guards. A forensics tool that prints 0 for a typo'd id is worse than no tool,
// so the refusals are asserted directly.
// ---------------------------------------------------------------------------

func TestForensicsGuardRejectsEmptyAndUnloadable(t *testing.T) {
	// No messages at all: the id resolved to a chat that never ran.
	empty := &runData{chat: &db.Chat{ID: "x"}}
	err := forensicsGuardInput("x", empty)
	if err == nil {
		t.Fatal("a chat with no messages must fail loudly, not report zeros")
	}
	if !strings.Contains(err.Error(), "no messages") {
		t.Fatalf("error should name the problem: %v", err)
	}

	// Messages with assistant turns but no content blocks: a failed block load
	// masquerading as a run that used no tools.
	rd := &runData{
		chat: &db.Chat{ID: "x"},
		messages: []*db.Message{
			{ID: "m1", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT},
		},
		blocksByMsg: map[string][]*db.MessageContentBlock{},
	}
	err = forensicsGuardInput("x", rd)
	if err == nil {
		t.Fatal("assistant turns with zero content blocks must fail loudly")
	}
	if !strings.Contains(err.Error(), "refusing to report an empty histogram") {
		t.Fatalf("error should refuse explicitly: %v", err)
	}

	// A user-only chat with no assistant turns is legitimately toolless.
	ok := &runData{
		chat:        &db.Chat{ID: "x"},
		messages:    []*db.Message{{ID: "m1", Role: reliantv1.MessageRole_MESSAGE_ROLE_USER}},
		blocksByMsg: map[string][]*db.MessageContentBlock{},
	}
	if err := forensicsGuardInput("x", ok); err != nil {
		t.Fatalf("a chat with no assistant turns is not an error: %v", err)
	}
}

func TestForensicsGuardRejectsZeroCallsAgainstRealTurns(t *testing.T) {
	rd := &runData{
		chat:     &db.Chat{ID: "x"},
		messages: []*db.Message{{ID: "m1", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT}},
		blocksByMsg: map[string][]*db.MessageContentBlock{
			// Blocks loaded fine, but none is a TOOL_CALL — the enum moved.
			"m1": {{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT}},
		},
	}
	err := forensicsGuardOutput("x", rd, forensicsReport{})
	if err == nil {
		t.Fatal("0 counted calls across real assistant turns must fail loudly")
	}
	if !strings.Contains(err.Error(), "refusing to report zero") {
		t.Fatalf("error should refuse explicitly: %v", err)
	}

	// A report with calls passes.
	good := forensicsReport{Totals: forensicsTotals{ToolCalls: 3}}
	if err := forensicsGuardOutput("x", rd, good); err != nil {
		t.Fatalf("a report with calls must not error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end over a synthetic run: the histogram, the read/write split, the
// repeat detection and the spawn linkage all come out of one build pass.
// ---------------------------------------------------------------------------

func TestBuildForensicsReportCountsAndLinks(t *testing.T) {
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	root, unit := "thread-root", "thread-unit"

	msg := func(id, thread string, role reliantv1.MessageRole, at time.Time) *db.Message {
		return &db.Message{ID: id, ThreadID: thread, Role: role, CreatedAt: at}
	}
	call := func(tool, input, callID string, at time.Time, pos int) *db.MessageContentBlock {
		b := &db.MessageContentBlock{
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolName:  strPtr(tool), CreatedAt: at, Position: pos,
		}
		if input != "" {
			b.ToolInput = strPtr(input)
		}
		if callID != "" {
			b.ToolCallID = strPtr(callID)
		}
		return b
	}
	result := func(tool string, at time.Time, pos int) *db.MessageContentBlock {
		return &db.MessageContentBlock{
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			ToolName:  strPtr(tool), CreatedAt: at, Position: pos,
		}
	}

	rd := &runData{
		chat: &db.Chat{ID: "chat-1"},
		workflows: []*db.Workflow{
			{ID: "wf-root", ChatID: "chat-1", Thread: root, WorkflowName: "builtin://forge-one-shot", CreatedAt: base},
			{ID: "wf-unit", ChatID: "chat-1", Thread: unit, ParentID: strPtr("wf-root"),
				SpawnedByNodeID: strPtr("spawn-call_ABC"), CreatedAt: base.Add(6 * time.Minute)},
		},
		messages: []*db.Message{
			msg("m1", root, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, base),
			msg("m2", root, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, base.Add(5*time.Minute)),
			msg("m3", unit, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, base.Add(7*time.Minute)),
		},
		blocksByMsg: map[string][]*db.MessageContentBlock{
			"m1": {
				call("view", `{"file_path":"/proj/a.go"}`, "", base, 0),
				result("view", base, 1),                                            // must not be counted
				call("view", `{"file_path":"a.go"}`, "", base.Add(time.Minute), 2), // same file, relative
				result("view", base.Add(time.Minute), 3),
				call("skill", `{"action":"load","path":"forge"}`, "", base.Add(2*time.Minute), 4),
			},
			"m2": {
				call("edit", `{"file_path":"/proj/a.go"}`, "", base.Add(5*time.Minute), 0),
				result("edit", base.Add(5*time.Minute), 1),
				call("spawn", `{"title":"unit-one","preset":"forge_implementer"}`, "call_ABC", base.Add(6*time.Minute), 2),
			},
			"m3": {
				call("view", `{"file_path":"/proj/a.go"}`, "", base.Add(7*time.Minute), 0),
				result("view", base.Add(7*time.Minute), 1),
				call("write", `{"file_path":"/proj/b.go"}`, "", base.Add(9*time.Minute), 2),
			},
		},
		stepsByWF: map[string][]*db.StepExecution{},
	}

	rep := buildForensicsReport("chat-1", rd, forensicsOpts{projectRoot: "/proj"})

	byThread := map[string]forensicsThread{}
	for _, th := range rep.Threads {
		byThread[th.Thread] = th
	}

	// Histogram counts CALLS only. Three TOOL_RESULT blocks for view exist; if
	// they were counted the root would show view 4 rather than 2.
	r := byThread[root]
	if r.ToolCalls != 5 {
		t.Fatalf("root tool calls = %d, want 5 (results excluded)", r.ToolCalls)
	}
	gotHist := map[string]int{}
	for _, h := range r.Histogram {
		gotHist[h.Tool] = h.Count
	}
	if gotHist["view"] != 2 {
		t.Fatalf("root view count = %d, want 2 — TOOL_RESULT blocks are being counted", gotHist["view"])
	}
	if gotHist["spawn"] != 1 || gotHist["edit"] != 1 || gotHist["skill"] != 1 {
		t.Fatalf("root histogram wrong: %v", gotHist)
	}

	// Reads/writes and the normalized repeat: /proj/a.go and a.go are one file.
	if r.Reads != 2 || r.Writes != 1 {
		t.Fatalf("root reads/writes = %d/%d, want 2/1", r.Reads, r.Writes)
	}
	if r.RepeatedCost != 1 || len(r.RepeatedReads) != 1 || r.RepeatedReads[0].Path != "a.go" {
		t.Fatalf("root repeated reads = %+v, want one repeat of a.go", r.RepeatedReads)
	}
	if r.ReadWriteRatio != 2 {
		t.Fatalf("root R:W = %v, want 2", r.ReadWriteRatio)
	}

	// Thread kinds come from the spawn relationship, never from names.
	if r.Kind != "root" {
		t.Fatalf("root kind = %q", r.Kind)
	}
	u := byThread[unit]
	if u.Kind != "unit" {
		t.Fatalf("unit kind = %q, want unit", u.Kind)
	}

	// The spawn is linked by tool_call_id, and the unit's first write is
	// measured from the spawn (base+6m) not from its first message (base+7m).
	if len(rep.SpawnTree) != 1 || !rep.SpawnTree[0].Linked {
		t.Fatalf("spawn tree = %+v, want one linked spawn", rep.SpawnTree)
	}
	if rep.SpawnTree[0].Title != "unit-one" {
		t.Fatalf("spawn title = %q", rep.SpawnTree[0].Title)
	}
	if u.FirstWriteFrom != "spawn" {
		t.Fatalf("unit first-write measured from %q, want spawn", u.FirstWriteFrom)
	}
	if u.TimeToFirstWriteMs != (3 * time.Minute).Milliseconds() {
		t.Fatalf("unit time to first write = %s, want 3m0s (spawn+6m -> write+9m)", u.TimeToFirstWrite)
	}

	// A file read in two threads is context failing to carry.
	if len(rep.CrossThread) != 1 || rep.CrossThread[0].Path != "a.go" || rep.CrossThread[0].Count != 3 {
		t.Fatalf("cross-thread repeats = %+v, want a.go x3", rep.CrossThread)
	}

	// Skill load offsets are relative to the thread's own start.
	if len(r.Skills) != 1 || r.Skills[0].Skill != "forge" {
		t.Fatalf("root skills = %+v", r.Skills)
	}
	if r.Skills[0].OffsetMs != (2 * time.Minute).Milliseconds() {
		t.Fatalf("skill offset = %s, want 2m0s", r.Skills[0].Offset)
	}

	// Cost was never recorded on any message — the report must say so rather
	// than claim a measured $0.00.
	if rep.Totals.CostRecorded {
		t.Fatal("no message carried a cost; CostRecorded must be false")
	}
	// 5 in the root (view, view, skill, edit, spawn) + 2 in the unit (view,
	// write). The six TOOL_RESULT blocks are excluded.
	if rep.Totals.ToolCalls != 7 {
		t.Fatalf("total tool calls = %d, want 7", rep.Totals.ToolCalls)
	}
}

// A thread that read and never wrote must not report a 0:1 ratio — "no writes"
// is a distinct finding and rendering it as zero inverts the meaning.
func TestForensicsNoWritesIsNotZeroRatio(t *testing.T) {
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	rd := &runData{
		chat:      &db.Chat{ID: "c"},
		workflows: []*db.Workflow{{ID: "wf", ChatID: "c", Thread: "t", CreatedAt: base}},
		messages: []*db.Message{
			{ID: "m1", ThreadID: "t", Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: base},
		},
		blocksByMsg: map[string][]*db.MessageContentBlock{
			"m1": {{
				BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
				ToolName:  strPtr("view"), ToolInput: strPtr(`{"file_path":"a.go"}`), CreatedAt: base,
			}},
		},
		stepsByWF: map[string][]*db.StepExecution{},
	}
	rep := buildForensicsReport("c", rd, forensicsOpts{})
	if len(rep.Threads) != 1 {
		t.Fatalf("want 1 thread, got %d", len(rep.Threads))
	}
	if rep.Threads[0].ReadWriteRatio != -1 {
		t.Fatalf("no-writes ratio = %v, want -1 sentinel", rep.Threads[0].ReadWriteRatio)
	}
	if rep.Threads[0].FirstWriteTool != "" {
		t.Fatalf("a thread that never wrote must have no first write, got %q", rep.Threads[0].FirstWriteTool)
	}
}
