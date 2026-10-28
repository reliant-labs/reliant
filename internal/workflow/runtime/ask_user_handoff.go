package runtime

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/core"
)

// A BLOCKING ask_user WAIT IS A PARK POINT FOR THE CONTINUE-AS-NEW HANDOFF.
//
// A background spawn parks for a handoff at its own iteration boundary
// (loop_executor.go); the soft handoff waits until every live spawn has. A
// spawn blocked in a blocking ask_user is inside its iteration, waiting on a
// person, so it never reached a boundary: the handoff waited on it, and every
// spawn launched meanwhile parked at iteration 0 and did nothing, until the
// hard backstop (chat b57e7021, a sub-agent's question at iteration 54).
//
// Carrying such a spawn from its last boundary does not work either: the
// successor re-runs CallLLM for that iteration, which fills the unanswered
// ask_user as interrupted, and the model asks a NEW question. So the spawn
// parks INSIDE the wait instead, with the iteration's pending tool batch: the
// node outputs its execute_tools started from, and what the batch had already
// settled (regular tool results, spawn handles, earlier answers). The
// successor seeds that iteration at its execute_tools, runs only the
// unsettled ask_user calls, and QuestionCreate's idempotency (same workflow,
// step, iteration and tool call) reattaches the SAME question, whose answer
// then resolves it. Nothing is written anywhere but the continuation input.
//
// The spawn keeps waiting while parked: an answer that arrives before the
// handoff is taken unparks it, and it parks again at its next boundary.

// askUserParksChangeID gates the park, asked once per observation and keyed
// by the history length it was observed at (replay-stable: the
// WorkflowTaskStarted event id). A history recorded before the park existed
// never asked, so every observation it replays answers DefaultVersion and the
// spawn keeps waiting, exactly as it did; the first observation made live
// records a marker and parks. That is what lets a run already stuck in this
// wait when the fix deploys hand off at its next workflow task.
const askUserParksChangeID = "ask-user-parks-for-handoff"

func askUserParksVersionID(questionID string, historyLength int) string {
	return fmt.Sprintf("%s/%s/%d", askUserParksChangeID, questionID, historyLength)
}

// PendingToolBatch is an agent-loop iteration stopped inside its execute_tools
// batch, waiting on a blocking ask_user, when a continue-as-new handoff took
// the spawn.
type PendingToolBatch struct {
	// NodeOutputs are the iteration's node outputs as they stood when the
	// batch started (call_llm's among them): the successor's iteration is
	// seeded with them, so the batch resolves the same tool calls.
	NodeOutputs map[string]interface{} `json:"node_outputs"`
	// TriggerStepID is the node whose completion triggered execute_tools.
	TriggerStepID string `json:"trigger_step_id"`
	// Progress is what the batch had settled.
	Progress toolBatchProgress `json:"progress"`
}

// toolBatchProgress is the settled part of a workflow-assembled tool batch.
type toolBatchProgress struct {
	Results       []interface{}          `json:"results,omitempty"`
	MessageOutput map[string]interface{} `json:"message_output,omitempty"`
	GrantedTools  []interface{}          `json:"granted_tools,omitempty"`
	// NextAsk indexes the batch's ask_user calls: the first one not settled.
	NextAsk int `json:"next_ask"`
}

func (p toolBatchProgress) snapshot() toolBatchProgress {
	p.Results = append([]interface{}(nil), p.Results...)
	return p
}

// toolBatchHandoff is what a background spawn's agent loop hands one
// iteration's StepExecutor: the spawn to park, and, for the iteration a
// successor re-enters, the batch progress to resume from.
type toolBatchHandoff struct {
	tracker   *ChildWorkflowTracker
	rec       *detachedSpawnRecord
	iteration int
	// resume is consumed by the iteration's execute_tools batch.
	resume *toolBatchProgress
}

// toolBatchPark is one execute_tools batch's view of its toolBatchHandoff.
type toolBatchPark struct {
	h             *toolBatchHandoff
	stepID        string
	triggerStepID string
	nodeOutputs   map[string]interface{}
}

// forBatch binds h to the execute_tools step stepID, started by trigger.
// Nil-receiver safe: a loop that is not a spawn's own has no handoff.
func (h *toolBatchHandoff) forBatch(stepID string, trigger *core.WorkflowEvent, nodeOutputs map[string]interface{}) *toolBatchPark {
	if h == nil || h.rec == nil {
		return nil
	}
	b := &toolBatchPark{h: h, stepID: stepID, nodeOutputs: nodeOutputs}
	if trigger != nil {
		b.triggerStepID = trigger.StepID
	}
	return b
}

type toolBatchHandoffKey struct{}

func withToolBatchHandoff(ctx workflow.Context, b *toolBatchPark) workflow.Context {
	return workflow.WithValue(ctx, toolBatchHandoffKey{}, b)
}

func toolBatchHandoffFrom(ctx workflow.Context) *toolBatchPark {
	b, _ := ctx.Value(toolBatchHandoffKey{}).(*toolBatchPark)
	return b
}

// takeResume returns the progress a successor resumes this batch from, once.
func (b *toolBatchPark) takeResume() *toolBatchProgress {
	if b == nil || b.h.resume == nil {
		return nil
	}
	p := b.h.resume
	b.h.resume = nil
	return p
}

// handoffRequested reports whether the run has asked its spawns to park. Read
// only: the tracker's request is raised by the loops that evaluate it.
func (b *toolBatchPark) handoffRequested() bool {
	return b != nil && b.h.tracker != nil && b.h.tracker.handoffRequested
}

// park records the spawn as parked inside this batch with progress settled.
func (b *toolBatchPark) park(progress toolBatchProgress) {
	outputs := make(map[string]interface{}, len(b.nodeOutputs))
	for k, v := range b.nodeOutputs {
		if k != b.stepID {
			outputs[k] = v
		}
	}
	rec := b.h.rec
	rec.parked = true
	rec.handoff.LoopIteration = b.h.iteration
	rec.handoff.AwaitSpawnsFirst = false
	rec.handoff.PendingToolBatch = &PendingToolBatch{
		NodeOutputs:   outputs,
		TriggerStepID: b.triggerStepID,
		Progress:      progress.snapshot(),
	}
}

// unpark undoes park: the wait ended before the handoff was taken.
func (b *toolBatchPark) unpark() {
	b.h.rec.parked = false
	b.h.rec.handoff.PendingToolBatch = nil
}

// askUserPark is what one blocking ask_user wait parks its spawn with.
type askUserPark struct {
	batch    *toolBatchPark
	progress toolBatchProgress
}

// awaitAskUserPark parks the spawn once a handoff is requested during the
// wait on questionID, and reports on the returned pointer whether it did.
// Cancel ctx to stop watching. It issues no command until it parks, and then
// only the version marker.
func awaitAskUserPark(ctx workflow.Context, questionID string, p *askUserPark) *bool {
	parked := new(bool)
	if p == nil || p.batch == nil {
		return parked
	}
	workflow.Go(ctx, func(gCtx workflow.Context) {
		observed := -1
		for {
			var length int
			if err := workflow.Await(gCtx, func() bool {
				length = workflow.GetInfo(gCtx).GetCurrentHistoryLength()
				return p.batch.handoffRequested() && length != observed
			}); err != nil {
				return
			}
			if workflow.GetVersion(gCtx, askUserParksVersionID(questionID, length), workflow.DefaultVersion, 1) == workflow.DefaultVersion {
				// Recorded before the park existed: that run kept waiting.
				observed = length
				continue
			}
			p.batch.park(p.progress)
			*parked = true
			workflow.GetLogger(gCtx).Info("[AskUser] Spawn parked in its question wait for continue-as-new handoff",
				"questionID", questionID, "toolCallID", p.batch.h.rec.ToolCallID, "iteration", p.batch.h.iteration)
			return
		}
	})
	return parked
}
