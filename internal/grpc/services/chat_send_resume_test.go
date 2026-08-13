// Copyright (c) 2025 Reliant Labs
package services

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plain-message question-resume answer MUST be wrapped with the exact resume
// marker so the resumed LLM knows its tool-call "answer" is a post-failure
// resume, not a direct answer. This is a fixed content contract.
func TestMarkResumeAnswer_ExactMarker(t *testing.T) {
	assert.Equal(t,
		"<system> workflow was canceled or failed. user resumed with message</system>: please continue",
		markResumeAnswer("please continue"))
}

// questionResumeResponseData must place the (marked) answer in answers[0].freetext
// so the workflow's parseQuestionResponse reads it as feedback and the marker
// reaches the LLM.
func TestQuestionResumeResponseData_CarriesMarkerAsFreetext(t *testing.T) {
	marked := markResumeAnswer("do the thing")
	data, err := questionResumeResponseData(marked)
	require.NoError(t, err)

	var parsed struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	require.NoError(t, json.Unmarshal([]byte(data), &parsed))
	require.Len(t, parsed.Answers, 1)
	assert.Equal(t, marked, parsed.Answers[0].Freetext,
		"the delivered answer freetext must carry the exact resume marker")
	assert.Empty(t, parsed.Answers[0].Selected,
		"no option is selected — a freetext answer is feedback, so the workflow loop continues")
}

// Resuming a paused chat touches two systems that cannot commit together —
// Postgres and Temporal — so the ORDER decides how a partial failure looks.
//
// The bug this pins: the status flip (`UpdateWorkflowStatus(... Running)`) used
// to run inside the message-save transaction, BEFORE the resume signal. When
// that write failed, SendMessage returned an error and never reached
// ResumeWorkflow, so the run stayed parked forever while the chat looked
// active. A serialization failure did exactly that to chat 80978aca: paused at
// 20:52, resume aborted at 20:55, still halted until an unrelated message
// happened to retry it 34 minutes later.
//
// Signalling first inverts the failure into a self-healing one: the run is
// genuinely awake, and the status is corrected by the best-effort write, the
// workflow's own Resumed notification, or the reconciler.
//
// This is asserted against the source because the ordering is the invariant —
// an integration test that mocked Temporal and Postgres could pass while the
// two calls sat in the wrong order.
func TestResumeSignalsBeforeMarkingRunning(t *testing.T) {
	src, err := os.ReadFile("chat_send.go")
	require.NoError(t, err)

	// Scope to the paused-chat resume branch so unrelated status writes
	// elsewhere in the file cannot satisfy or break this.
	body := string(src)
	start := strings.Index(body, "case db.WorkflowStatusPaused:")
	require.Positive(t, start, "paused-resume branch not found")
	end := strings.Index(body[start:], "\n\t\t\tcase ")
	if end == -1 {
		end = len(body) - start
	}
	branch := body[start : start+end]

	signalAt := strings.Index(branch, "s.pauseService.ResumeWorkflow(")
	require.Positive(t, signalAt, "the paused branch must resume via PauseService")

	markRunningAt := strings.Index(branch, "db.WorkflowStatusRunning)")
	require.Positive(t, markRunningAt, "the paused branch must mark the workflow running")

	assert.Less(t, signalAt, markRunningAt,
		"ResumeWorkflow must be called BEFORE the workflow is marked running — "+
			"marking first means a failed DB write aborts the request without ever "+
			"signalling, leaving the run parked while the UI shows it active")

	// The save transaction must not carry the status flip: anything inside it
	// can abort the request before the signal is sent.
	txStart := strings.Index(branch, "s.database.RunTx(")
	require.Positive(t, txStart, "the paused branch must save messages in a transaction")
	txEnd := strings.Index(branch[txStart:], "\n\t\t\t\t})")
	require.Positive(t, txEnd, "could not delimit the save transaction")
	saveTx := branch[txStart : txStart+txEnd]

	assert.NotContains(t, saveTx, "UpdateWorkflowStatus",
		"the status flip must live outside the message-save transaction, or a "+
			"failure there blocks the resume signal entirely")
}

// A failed resume must be REPORTED, not logged and papered over. Returning
// workflow_status=running after the signal failed is what makes a chat look
// active while nothing is executing — the message is saved, the UI shows work
// in progress, and the run is still parked.
func TestFailedResumeIsReportedNotSwallowed(t *testing.T) {
	src, err := os.ReadFile("chat_send.go")
	require.NoError(t, err)

	body := string(src)
	at := strings.Index(body, "s.pauseService.ResumeWorkflow(")
	require.Positive(t, at, "resume call not found")

	// Look at the error handling immediately following the resume call.
	window := body[at:min(at+1600, len(body))]
	assert.Contains(t, window, "connect.NewError(connect.CodeInternal",
		"a resume failure must surface to the caller so the user's client can retry; "+
			"the saved message makes the retry safe")
}
