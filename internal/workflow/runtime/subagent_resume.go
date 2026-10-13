// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"maps"
	"sort"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A sub-agent that stopped on a failure — its step's retries exhausted, a
// provider error, a usage limit — is stopped, not finished. Failing alone
// (subagent_failure.go) keeps one agent's trouble from pausing the whole run;
// it must not also make that trouble permanent. On 2026-10-13 (workflow
// 9fbaace8) a sub-agent hit Anthropic's 7-day usage limit, ended "failed", and
// every message to it afterwards was refused: a transient condition had
// killed the thread for good.
//
// So a message sent to a stopped sub-agent resumes it. Both senders — the
// user's SendAgentMessage and the parent's spawn_send — queue the message and
// ring the agent's doorbell (ThreadWakeSignalName). The run answers the
// doorbell of a stopped agent by relaunching it exactly as it relaunches a
// spawn carried across a continue-as-new: same tool call, same thread, same
// child workflow row, no new thread and no seed message; the thread's history
// is where it left off. Its first CallLLM drains the message. Its new outcome
// reaches its parent as any spawn's does, through the parent's mailbox.
//
// Truly finished agents stay finished: one that completed, and one the user
// stopped, are never recorded as stopped and never resumed.

// resumeStoppedSubAgentChangeID versions every place a message now resumes,
// or retries, a sub-agent that would otherwise have ended (workflow.GetVersion):
// the wake that relaunches a stopped agent, the in-place retry of a failing
// step a message is waiting on, and the refusal to start a second loop on an
// agent that is already running. Histories recorded before it did none of
// these and must replay the same.
const resumeStoppedSubAgentChangeID = "message-resumes-stopped-subagent"

// recordStoppedSpawn remembers the live spawn toolCallID as stopped on a
// failure. Called while it is still registered live, so its handoff — the
// relaunch recipe — is still there to keep.
func (t *ChildWorkflowTracker) recordStoppedSpawn(toolCallID string) {
	rec := t.liveDetachedSpawns[toolCallID]
	if rec == nil || rec.ChildThread == "" {
		return
	}
	if t.stoppedSpawns == nil {
		t.stoppedSpawns = make(map[string]*detachedSpawnRecord)
	}
	stopped := *rec
	stopped.parked = false
	stopped.resumeArmed = false
	t.stoppedSpawns[rec.ChildThread] = &stopped
}

// stoppedSpawnFor returns the stopped sub-agent on thread, or nil.
func (t *ChildWorkflowTracker) stoppedSpawnFor(thread string) *detachedSpawnRecord {
	if t == nil {
		return nil
	}
	return t.stoppedSpawns[thread]
}

// forgetStoppedSpawn drops thread's stopped record: it was resumed.
func (t *ChildWorkflowTracker) forgetStoppedSpawn(thread string) {
	if t == nil {
		return
	}
	delete(t.stoppedSpawns, thread)
	t.forgetLastThreadInputs(thread)
}

// forgetLastThreadInputs drops the inputs kept for a spawn that finished, or
// that is running again and holds live ones.
func (t *ChildWorkflowTracker) forgetLastThreadInputs(thread string) {
	if t == nil {
		return
	}
	delete(t.lastThreadInputs, thread)
}

// stoppedSpawnHandoffs snapshots the stopped sub-agents for a continue-as-new,
// sorted by thread so the continuation input is identical on replay.
func (t *ChildWorkflowTracker) stoppedSpawnHandoffs() []SpawnHandoff {
	if len(t.stoppedSpawns) == 0 {
		return nil
	}
	threads := make([]string, 0, len(t.stoppedSpawns))
	for thread := range t.stoppedSpawns {
		threads = append(threads, thread)
	}
	sort.Strings(threads)
	out := make([]SpawnHandoff, 0, len(threads))
	for _, thread := range threads {
		out = append(out, t.stoppedSpawns[thread].handoff)
	}
	return out
}

// seedStoppedSpawns restores the stopped sub-agents a predecessor carried.
func (t *ChildWorkflowTracker) seedStoppedSpawns(handoffs []SpawnHandoff) {
	for _, h := range handoffs {
		if h.ToolCallID == "" || h.ChildThread == "" || h.ParentThread == "" {
			continue
		}
		if t.stoppedSpawns == nil {
			t.stoppedSpawns = make(map[string]*detachedSpawnRecord)
		}
		t.stoppedSpawns[h.ChildThread] = &detachedSpawnRecord{
			ToolCallID:   h.ToolCallID,
			ParentThread: h.ParentThread,
			ChildThread:  h.ChildThread,
			handoff:      h,
		}
	}
}

// recordThreadInputOverride remembers params the user set on thread alone.
// Later updates win key by key.
func (t *ChildWorkflowTracker) recordThreadInputOverride(thread string, update map[string]interface{}) {
	if thread == "" || len(update) == 0 {
		return
	}
	if t.threadInputOverrides == nil {
		t.threadInputOverrides = make(map[string]map[string]interface{})
	}
	if t.threadInputOverrides[thread] == nil {
		t.threadInputOverrides[thread] = make(map[string]interface{}, len(update))
	}
	maps.Copy(t.threadInputOverrides[thread], update)
}

// applyThreadInputOverrides writes what the user set on thread over inputs,
// and marks those keys as the thread's own so a global update does not undo
// them. This is syncIterationInputs' rule for a thread that is rebuilt rather
// than re-dispatched in place: the run that follows uses what the user chose
// while it was stopped, not what it started with.
func (t *ChildWorkflowTracker) applyThreadInputOverrides(thread string, inputs map[string]interface{}, ownedKeys map[string]bool) {
	if t == nil || inputs == nil {
		return
	}
	for key, value := range t.threadInputOverrides[thread] {
		inputs[key] = value
		if ownedKeys != nil {
			ownedKeys[key] = true
		}
	}
}

// threadInputOverridesHandoff snapshots the per-thread overrides for a
// continue-as-new.
func (t *ChildWorkflowTracker) threadInputOverridesHandoff() map[string]map[string]interface{} {
	if len(t.threadInputOverrides) == 0 {
		return nil
	}
	out := make(map[string]map[string]interface{}, len(t.threadInputOverrides))
	for thread, overrides := range t.threadInputOverrides {
		out[thread] = maps.Clone(overrides)
	}
	return out
}

// seedThreadInputOverrides restores the per-thread overrides a predecessor
// carried.
func (t *ChildWorkflowTracker) seedThreadInputOverrides(overrides map[string]map[string]interface{}) {
	for thread, update := range overrides {
		t.recordThreadInputOverride(thread, update)
	}
}

// stoppedThreadInputs is what a stopped sub-agent will resume with: the inputs
// it stopped with, under what the user has changed since. Nil when thread is
// not a stopped sub-agent.
func (t *ChildWorkflowTracker) stoppedThreadInputs(thread string) map[string]interface{} {
	if t.stoppedSpawnFor(thread) == nil {
		return nil
	}
	inputs := maps.Clone(t.lastThreadInputs[thread])
	if inputs == nil {
		inputs = make(map[string]interface{})
	}
	t.applyThreadInputOverrides(thread, inputs, nil)
	return inputs
}

// stoppedSpawnResumeDeps is what resuming a stopped sub-agent needs from the
// execution that runs it.
type stoppedSpawnResumeDeps struct {
	// rootThread is the run's own thread: a parent that is always there to
	// read a report while the run is.
	rootThread          string
	chatID              string
	projectPath         string
	workflowInputs      map[string]interface{}
	childTracker        *ChildWorkflowTracker
	cancelledThreads    map[string]bool
	makeThreadPauseCtrl func(string) *PauseController
	makeThreadInterrupt func(string) *ThreadInterrupt
}

// resumeStoppedSpawn resumes the sub-agent on thread if it stopped on a
// failure, and reports whether it did. Called when the agent's doorbell
// rings: something queued a message for it.
//
// It does not resume an agent whose parent has finished. The resumed agent's
// outcome is reported to that parent's mailbox and the parent's loop is what
// waits for it; with no parent left, the outcome would go nowhere and the
// agent would run unwatched.
//
// The resumed run reports under a fresh slot (SpawnHandoff.ReportID): its
// tool call's one terminal report slot already holds the failure it stopped
// with, and a second report there is dropped as a duplicate.
func resumeStoppedSpawn(ctx workflow.Context, thread string, deps stoppedSpawnResumeDeps) bool {
	tracker := deps.childTracker
	stopped := tracker.stoppedSpawnFor(thread)
	if stopped == nil || tracker.liveSpawnFor(thread) != nil {
		return false
	}
	logger := workflow.GetLogger(ctx)
	h := stopped.handoff
	if h.ParentThread != deps.rootThread && tracker.liveSpawnFor(h.ParentThread) == nil {
		logger.Warn("[SpawnResume] Not resuming a stopped sub-agent: the parent it reports to has finished",
			"childThread", h.ChildThread,
			"parentThread", h.ParentThread,
			"toolCallID", h.ToolCallID,
		)
		return false
	}
	if workflow.GetVersion(ctx, resumeStoppedSubAgentChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return false
	}

	tracker.forgetStoppedSpawn(thread)
	// The resumed run's first turn reads everything rung for it so far. Were
	// it to fail before taking that turn, those rings must not resume it
	// again — one message buys one resume.
	tracker.recordTurnStart(thread)
	// A stop aimed at the run that failed does not carry over to the one a
	// message has just asked for. (A sub-agent the user stopped is cancelled,
	// not stopped on a failure, and is never resumed.)
	delete(deps.cancelledThreads, h.ChildThread)
	delete(deps.cancelledThreads, h.ToolCallID)

	h.Resumes++
	h.ReportID = fmt.Sprintf("%s#resume-%d", h.ToolCallID, h.Resumes)
	h.LoopIteration = 0
	h.AwaitSpawnsFirst = false
	h.Cancelled = false

	config := &spawnChildWorkflowConfig{
		toolCallID:      h.ToolCallID,
		childWorkflowID: h.ChildWorkflowID,
		childThread:     h.ChildThread,
		presetName:      h.Preset,
		title:           h.Title,
		isResumption:    true,
		reportID:        h.ReportID,
	}
	if config.childWorkflowID == "" {
		config.childWorkflowID = DeterministicWorkflowID(h.ParentWorkflowID, h.ToolCallID)
	}
	prep := prepareSpawnRelaunch(config, h, deps.chatID, deps.projectPath, deps.workflowInputs, deps.makeThreadPauseCtrl)
	// Live from this moment, so a second doorbell finds it running, and its
	// parent's loop waits for its outcome.
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID:   h.ToolCallID,
		ChatID:       deps.chatID,
		ParentThread: h.ParentThread,
		ChildThread:  h.ChildThread,
		handoff:      h,
	})
	logger.Info("[SpawnResume] Resuming a sub-agent that stopped on a failure, for a message sent to it",
		"childThread", h.ChildThread,
		"parentThread", h.ParentThread,
		"toolCallID", h.ToolCallID,
		"reportID", h.ReportID,
	)

	// Off the caller's coroutine: the caller is the doorbell's receiver, and
	// every other thread's wakes queue behind it.
	workflow.Go(ctx, func(gCtx workflow.Context) {
		// The agent's own row and its thread go back to running — "started"
		// on a terminal thread revives it — and with the row, the agent's
		// spawn card, which reads it. The spawn TOOL CALL is not touched: it
		// ended, failed, and a call ends once (see notifySpawnTerminal).
		notifyWorkflowStatus(gCtx, deps.chatID, config.childWorkflowID, prep.targetWorkflow, "started", h.ParentWorkflowID, h.ChildThread, &workflowStatusOpts{
			Title:               h.Preset,
			ThreadTitle:         prep.threadTitle,
			Origin:              model.ThreadOriginSpawn,
			SpawnedByToolCallID: h.ToolCallID,
		})
		startDetachedSpawn(gCtx, config, deps.chatID, h.ParentWorkflowID, h.ParentThread, deps.projectPath, deps.workflowInputs,
			tracker, deps.makeThreadPauseCtrl, deps.makeThreadInterrupt, prep)
	})
	return true
}

// refuseResumingLiveAgent refuses spawn(agent_id=...) for an agent that is
// already running, or returns nil. One agent is one loop on one thread: a
// second loop would interleave two conversations in one history. A message
// can resume a stopped agent behind its parent's back, so a parent acting on
// the failure report it got earlier can ask for exactly that.
func refuseResumingLiveAgent(ctx workflow.Context, config *spawnChildWorkflowConfig, tracker *ChildWorkflowTracker) *spawnInlineResult {
	if !config.isResumption || tracker.liveSpawnFor(config.childThread) == nil {
		return nil
	}
	if workflow.GetVersion(ctx, resumeStoppedSubAgentChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	return &spawnInlineResult{
		ToolCallID: config.toolCallID,
		IsError:    true,
		Content: fmt.Sprintf(
			"Agent %q is already running (a message sent to it may have resumed it), so it cannot be resumed again. "+
				"Use spawn_send(agent_id=%q, ...) to give it new instructions, or spawn_status to check on it.",
			config.childThread, config.childThread),
	}
}

// retriesForUnreadMessage decides whether a sub-agent step that has just
// failed is re-dispatched in place instead of ending the agent: it is when a
// message reached the agent after its current turn began. No turn has read
// that message, and ending now would strand it behind a stopped agent — the
// sub-agent form of the rule late_wake.go applies to a run that is finishing.
// The retry is the turn the message is owed; when it fails too, the agent
// ends as it would have.
//
// It also shows the failure on the agent's thread, so the retry is not
// silent.
func retriesForUnreadMessage(ctx workflow.Context, tracker *ChildWorkflowTracker, exhaustion retryExhaustionError) bool {
	thread := exhaustion.Thread
	if tracker == nil || thread == "" || !tracker.wakeSinceLastTurn(thread) {
		return false
	}
	if workflow.GetVersion(ctx, resumeStoppedSubAgentChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return false
	}
	// The retry reads everything rung so far; only a newer message earns
	// another.
	tracker.recordTurnStart(thread)

	exhaustion.Summary = oneLineError(exhaustion.Err) + ". Retrying to answer the message sent to this agent."
	errorCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	_ = workflow.ExecuteActivity(errorCtx, "WorkflowError", exhaustion.payload()).Get(ctx, nil)
	return true
}
