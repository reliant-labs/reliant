// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
		Status:       db.WorkflowStatusCompleted,
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
