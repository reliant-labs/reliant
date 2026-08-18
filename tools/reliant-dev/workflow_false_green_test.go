// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/execfollow"
)

// Run e10cabae executed nothing, failed every gate lane five times, and routed
// to its workflow's `failed` terminal node — and `workflow analyze` printed
// status: completed. These pin the analyze surface; the `workflow status` and
// wait-for-gate halves stayed with the reliant CLI, which owns those commands.

func strPtr(s string) *string { return &s }

// analyzeRunData builds the minimal runData the report builder needs: one root
// execution plus the chat it belongs to.
func analyzeRunData(chatWorkflow string, root *db.Workflow) *runData {
	rd := &runData{
		chat:            &db.Chat{ID: "e10cabae", Title: "peptides", CreatedAt: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)},
		workflows:       []*db.Workflow{root},
		workflowByID:    map[string]*db.Workflow{root.ID: root},
		byThread:        map[string]*db.Workflow{root.Thread: root},
		stepsByWF:       map[string][]*db.StepExecution{},
		blocksByMsg:     map[string][]*db.MessageContentBlock{},
		backoffByThread: map[string]db.ProviderBackoff{},
	}
	if chatWorkflow != "" {
		rd.chat.WorkflowName = &chatWorkflow
	}
	return rd
}

func failedRoot() *db.Workflow {
	completed := time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	return &db.Workflow{
		ID:           "wf-root",
		ChatID:       "e10cabae",
		WorkflowName: "builtin://forge-one-shot",
		Thread:       "t-root",
		Status:       db.Completed(),
		CreatedAt:    time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC),
		CompletedAt:  &completed,
		Outcome:      strPtr(execfollow.OutcomeFailure),
	}
}

func TestAnalyzeDoesNotReportAFailedRunAsCompleteSuccess(t *testing.T) {
	// The chat has since transitioned to builtin://agent (forge-one-shot
	// declares transition_to), exactly as on the measured run.
	rd := analyzeRunData("builtin://agent", failedRoot())
	rep := buildAnalyzeReport("e10cabae", rd)

	if rep.Overview.Outcome != execfollow.OutcomeFailure {
		t.Fatalf("overview outcome = %q, want %q", rep.Overview.Outcome, execfollow.OutcomeFailure)
	}

	var out bytes.Buffer
	printAnalyzeReport(&out, rep)
	text := out.String()

	if !strings.Contains(text, "outcome:") || !strings.Contains(text, "FAILURE") {
		t.Fatalf("analyze header does not say the run failed:\n%s", text)
	}
	// The phases table must not describe the root phase as a plain completion.
	if !strings.Contains(text, "NOT-PASSED") {
		t.Fatalf("phases table renders the failing root as a normal completion:\n%s", text)
	}
}

// TestAnalyzeHeaderNamesTheWorkflowThatRan pins the header/phases contradiction:
// the first line said `workflow: builtin://agent` for a run whose own phases
// table said builtin://forge-one-shot, because the header read the CHAT's
// workflow_name — which transition_to rewrites when the pipeline ends.
func TestAnalyzeHeaderNamesTheWorkflowThatRan(t *testing.T) {
	rd := analyzeRunData("builtin://agent", failedRoot())
	rep := buildAnalyzeReport("e10cabae", rd)

	var out bytes.Buffer
	printAnalyzeReport(&out, rep)
	header := strings.SplitN(out.String(), "\n", 3)[1] // "  workflow: ...   status: ..."

	if !strings.Contains(header, "builtin://forge-one-shot") {
		t.Fatalf("header names the wrong workflow: %q", header)
	}
	if strings.Contains(header, "builtin://agent") {
		t.Fatalf("header names the chat's CURRENT workflow, not the one that ran: %q", header)
	}
	// The transition is still reported — it is real information, just not the
	// answer to "what ran".
	if !strings.Contains(out.String(), "chat now: builtin://agent") {
		t.Fatalf("the chat's later transition was dropped entirely:\n%s", out.String())
	}
}

// ---------------------------------------------------------------------------
// SKILL LOADS: a section that measured one of two delivery paths and rendered
// the other one's absence as absence of skills.
//
// Run a70a9606's scaffold phase ran under forge-one-shot.yaml, whose
// scaffold_and_verify node preloads six skills via its `skills:` arg
// (forge/getting-started, db, forge/proto, forge/architecture, service-layer,
// forge/frontend/design). The agent's own transcript says "All six phase skills
// are already preloaded." forensics printed:
//
//	SKILL LOADS
//	  none — no thread loaded a skill
//
// which was read as "no skills reached the agent" and published as a knowledge
// gap. The counter only ever saw `skill` TOOL_CALL blocks, and a preloaded
// skill produces none: buildSeededSkillMessages splices the bodies into the
// in-memory history for that one LLM call and nothing writes them
// (call_llm.go: "EPHEMERAL — re-seeded each turn, never persisted"). No column
// holds the arg either.
//
// So the honest fix is not a count — it is refusing to let zero explicit calls
// stand unqualified. These tests pin that.
// ---------------------------------------------------------------------------

// forensicsRunWithSkillCalls builds a one-thread run whose assistant turn makes
// `skillCalls` explicit `skill` tool calls (plus one `view`, so the tool-call
// guard has something to count either way).
func forensicsRunWithSkillCalls(skillCalls int) *runData {
	base := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	blocks := []*db.MessageContentBlock{{
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:  strPtr("view"),
		ToolInput: strPtr(`{"file_path":"a.go"}`),
		CreatedAt: base,
	}}
	for i := 0; i < skillCalls; i++ {
		blocks = append(blocks, &db.MessageContentBlock{
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolName:  strPtr("skill"),
			ToolInput: strPtr(`{"action":"load","path":"testing"}`),
			CreatedAt: base.Add(time.Duration(i+1) * time.Minute),
			Position:  i + 1,
		})
	}
	return &runData{
		chat: &db.Chat{ID: "a70a9606"},
		workflows: []*db.Workflow{{
			ID: "wf-root", ChatID: "a70a9606", Thread: "t-scaffold",
			WorkflowName: "builtin://forge-one-shot", CreatedAt: base,
		}},
		messages: []*db.Message{{
			ID: "m1", ThreadID: "t-scaffold",
			Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: base,
		}},
		blocksByMsg: map[string][]*db.MessageContentBlock{"m1": blocks},
		stepsByWF:   map[string][]*db.StepExecution{},
	}
}

// TestForensicsSkillsZeroExplicitCallsIsNotReportedAsNoSkills is the regression
// test for the wrong finding. A run that made no `skill` tool call must not be
// described in a way that supports "this agent had no guidance".
func TestForensicsSkillsZeroExplicitCallsIsNotReportedAsNoSkills(t *testing.T) {
	rep := buildForensicsReport("a70a9606", forensicsRunWithSkillCalls(0), forensicsOpts{})

	if rep.SkillDelivery.ExplicitCalls != 0 {
		t.Fatalf("explicit calls = %d, want 0", rep.SkillDelivery.ExplicitCalls)
	}
	// The report must CLAIM the blind spot, not leave it to be inferred.
	if rep.SkillDelivery.PreloadObservable {
		t.Fatal("preloaded skills are never persisted; the report must not claim they are observable")
	}

	var out bytes.Buffer
	printForensicsReport(&out, rep, forensicsOpts{})
	text := out.String()

	// The exact string that caused the wrong conclusion.
	if strings.Contains(text, "none — no thread loaded a skill") {
		t.Fatalf("SKILL LOADS still asserts no skills reached the run — a preloaded skill makes that false:\n%s", text)
	}
	// It must scope the zero to the mechanism it actually measured...
	if !strings.Contains(text, "explicit skill tool calls: 0") {
		t.Fatalf("zero is not scoped to explicit tool calls:\n%s", text)
	}
	// ...and name the mechanism it could not see, using words a reader can act
	// on rather than a bare caveat.
	for _, want := range []string{"preload", "NOT visible", "never persisted"} {
		if !strings.Contains(text, want) {
			t.Fatalf("SKILL LOADS does not state the preload blind spot (missing %q):\n%s", want, text)
		}
	}
}

// TestForensicsSkillsExplicitLoadsStillReported keeps state 2 intact: the
// existing per-thread listing with offsets must survive, and must not be
// silently converted into "these are all the skills this run had".
func TestForensicsSkillsExplicitLoadsStillReported(t *testing.T) {
	rep := buildForensicsReport("a70a9606", forensicsRunWithSkillCalls(2), forensicsOpts{})

	if rep.SkillDelivery.ExplicitCalls != 2 {
		t.Fatalf("explicit calls = %d, want 2", rep.SkillDelivery.ExplicitCalls)
	}
	if len(rep.Threads) != 1 || len(rep.Threads[0].Skills) != 2 {
		t.Fatalf("per-thread skill loads lost: %+v", rep.Threads)
	}

	var out bytes.Buffer
	printForensicsReport(&out, rep, forensicsOpts{})
	text := out.String()

	if !strings.Contains(text, "testing") {
		t.Fatalf("the loaded skill is no longer named:\n%s", text)
	}
	if !strings.Contains(text, "explicit skill tool calls: 2") {
		t.Fatalf("explicit call count missing:\n%s", text)
	}
	// A populated list is just as quotable as an empty one. Six preloaded
	// skills alongside two explicit loads must not read as "two skills".
	if !strings.Contains(text, "preload") {
		t.Fatalf("a populated SKILL LOADS section drops the preload caveat, so its list reads as exhaustive:\n%s", text)
	}
}

// TestForensicsSkillSectionHeaderNamesWhatItCounts pins the header itself. The
// section is quoted by title in reports, so "SKILL LOADS" alone overclaims.
func TestForensicsSkillSectionHeaderNamesWhatItCounts(t *testing.T) {
	var out bytes.Buffer
	printForensicsReport(&out, buildForensicsReport("a70a9606", forensicsRunWithSkillCalls(1), forensicsOpts{}), forensicsOpts{})

	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "SKILL LOADS") {
			if !strings.Contains(line, "explicit") {
				t.Fatalf("section header does not say it counts only explicit calls: %q", line)
			}
			return
		}
	}
	t.Fatal("SKILL LOADS section is missing entirely")
}

func TestAnalyzeUndeclaredOutcomeIsNotRenderedAsFailure(t *testing.T) {
	root := failedRoot()
	root.Outcome = nil
	rd := analyzeRunData("builtin://agent", root)
	rep := buildAnalyzeReport("e10cabae", rd)

	var out bytes.Buffer
	printAnalyzeReport(&out, rep)
	if strings.Contains(out.String(), "FAILURE") {
		t.Fatalf("a workflow that declares no verdict was rendered as failing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "not declared") {
		t.Fatalf("expected the absent verdict to be stated as absent:\n%s", out.String())
	}
}
