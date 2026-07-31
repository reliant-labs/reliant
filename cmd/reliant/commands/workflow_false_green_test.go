// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/execfollow"
)

// Run e10cabae executed nothing, failed every gate lane five times, and routed
// to its workflow's `failed` terminal node — and `workflow status` printed
// COMPLETED and exited 0. These pin the status and wait-for-gate surfaces; the
// `workflow analyze` half moved with that command to tools/reliant-dev.

func strPtr(s string) *string { return &s }

// --- workflow status -------------------------------------------------------

func TestStatusReportSaysTheRunDidNotPass(t *testing.T) {
	r := statusReport{
		ExecutionID: "e10cabae",
		Workflow:    "builtin://forge-one-shot",
		Status:      "completed",
		Outcome:     execfollow.OutcomeFailure,
	}

	var out bytes.Buffer
	printStatusReport(&out, r)
	text := out.String()

	if !strings.Contains(text, "Outcome:") {
		t.Fatalf("status printed no outcome at all:\n%s", text)
	}
	if !strings.Contains(text, "FAILURE") {
		t.Fatalf("a run that ended at its failure terminal did not say so:\n%s", text)
	}
	// The lifecycle is still reported — the two facts must both survive, not
	// replace each other.
	if !strings.Contains(text, "Status:    COMPLETED") {
		t.Fatalf("the lifecycle status was lost:\n%s", text)
	}
}

func TestStatusExitCodeDistinguishesRanFromPassed(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		outcome string
		want    int
	}{
		{"ended at failure terminal", "completed", execfollow.OutcomeFailure, 2},
		{"ended at success terminal", "completed", execfollow.OutcomeSuccess, 0},
		{"completed, no verdict declared", "completed", "", 0},
		{"still running", "running", "", 0},
		{"paused at a gate", "paused", "", 0},
		{"workflow crashed", "failed", "", 2},
		{"user cancelled", "cancelled", "", 2},
		{"expired", "expired", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statusExitCode(statusReport{Status: tc.status, Outcome: tc.outcome})
			if got != tc.want {
				t.Fatalf("statusExitCode(status=%q, outcome=%q) = %d, want %d",
					tc.status, tc.outcome, got, tc.want)
			}
		})
	}
}

// TestStatusJSONCarriesOutcome proves the verdict survives the RPC, end to end
// through the real command. Uses a SUCCESS outcome deliberately: the failure
// path exits the process (exit 2), which is the point of the exit-code test
// above and cannot be exercised in-process.
func TestStatusJSONCarriesOutcome(t *testing.T) {
	h := newSuperviseHarness(t)
	h.chat.root = &reliantv1.WorkflowExecution{
		Id:           "chat-1",
		WorkflowName: "builtin://forge-one-shot",
		Status:       reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_COMPLETED,
		CreatedAt:    "2026-07-22T19:57:02Z",
		Outcome:      strPtr(execfollow.OutcomeSuccess),
	}

	stdout, _, err := h.run(t, "", "workflow", "status", "chat-1", "--json")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("status output not JSON: %v (%s)", err, stdout)
	}
	if report.Outcome != execfollow.OutcomeSuccess {
		t.Fatalf("outcome = %q, want %q — the verdict did not survive the RPC", report.Outcome, execfollow.OutcomeSuccess)
	}
}

// --- wait-for-gate ---------------------------------------------------------

func TestWaitForGateReportsDidNotPass(t *testing.T) {
	var out bytes.Buffer
	if err := writeWaitForGateResult(&out, "e10cabae", execfollow.ExitFailed, nil,
		"completed", execfollow.OutcomeFailure, true); err != nil {
		t.Fatalf("writeWaitForGateResult: %v", err)
	}
	var res waitForGateResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, out.String())
	}
	if res.Outcome == "completed" {
		t.Fatalf("a run that did not pass reported outcome %q", res.Outcome)
	}
	if res.Outcome != "did_not_pass" {
		t.Fatalf("outcome = %q, want did_not_pass", res.Outcome)
	}
}
