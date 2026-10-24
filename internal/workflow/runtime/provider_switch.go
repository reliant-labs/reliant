// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"reflect"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db"
)

const (
	// providerSwitchChangeID versions the changed command paths when a model
	// picker update reaches a run. Asked per update so a run parked at deploy
	// can accept its first live switch without replaying old switches anew.
	providerSwitchChangeID = "chat-provider-switch-preserve-tags"
	// InputKeyProviderSwitch carries the strict driver chosen for this chat.
	// It is runtime-injected, not a client-settable workflow input.
	InputKeyProviderSwitch = "__chat_provider"
	inputKeyPresetModel    = "__preset_model"
)

// providerVersion stops creating marker IDs after the first new-code answer.
// During old-history replay, DefaultVersion advances the observation index;
// the first live event records one marker, and all later events reuse it.
func (t *ChildWorkflowTracker) providerVersion(ctx workflow.Context, category string, observation int) bool {
	if t.providerVersions[category] {
		return true
	}
	if workflow.GetVersion(ctx, fmt.Sprintf("%s/%s/%d", providerSwitchChangeID, category, observation), workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return false
	}
	if t.providerVersions == nil {
		t.providerVersions = map[string]bool{}
	}
	t.providerVersions[category] = true
	return true
}

// boundProvider reads the chat's provider binding from an update or a run's
// inputs: the driver, and whether the key is there at all. Only the server
// writes it (an explicit provider switch binds, a local root pick sends ""
// to unbind; chat_provider_binding.go). A model selector that happens to name
// a provider binds nothing: an ordinary pick or send is not a switch.
func boundProvider(inputs map[string]interface{}) (driver string, present bool) {
	value, present := inputs[InputKeyProviderSwitch]
	driver, _ = value.(string)
	return driver, present
}

// applyProviderSwitch applies a binding change an update carries, and reports
// whether the binding changed. It changes routing, not the descendants'
// semantic model selectors. All executors share the tracker, so active,
// nested and future spawns see the driver at their next model call. Inputs
// carry it across a continue-as-new or fresh resumption. No activity or tool
// is cancelled.
func (t *ChildWorkflowTracker) applyProviderSwitch(ctx workflow.Context, inputs map[string]interface{}, update map[string]interface{}) bool {
	_, modelChanged := update["model"]
	_, bindingPresent := update[InputKeyProviderSwitch]
	if !modelChanged && !bindingPresent {
		return false
	}
	if workflow.GetVersion(ctx, providerSwitchChangeID+"/explicit-binding", workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		if !modelChanged {
			return false
		}
		return t.applyLegacyProviderSwitch(ctx, inputs, update)
	}
	driver, present := boundProvider(update)
	if !present || driver == t.chatProvider {
		// Repeated params are not a new provider choice: a normal message
		// must not reset routing leases merely because it repeats them.
		return false
	}
	t.providerUpdates++
	if !t.providerVersion(ctx, "update", t.providerUpdates) {
		return false
	}
	t.chatProvider = driver
	t.providerPinCleanups = map[string]bool{}
	if driver == "" {
		delete(inputs, InputKeyProviderSwitch)
	} else {
		inputs[InputKeyProviderSwitch] = driver
	}
	return true
}

// applyLegacyProviderSwitch preserves the commands recorded by the original
// switch implementation. New executions use only server-written bindings.
func (t *ChildWorkflowTracker) applyLegacyProviderSwitch(ctx workflow.Context, inputs, update map[string]interface{}) bool {
	selected := func(values map[string]interface{}) string {
		if driver, _ := values[InputKeyProviderSwitch].(string); driver != "" {
			return driver
		}
		if own := explicitModelProviders(values["model"]); len(own) == 1 {
			return own[0]
		}
		return ""
	}
	driver := selected(update)
	if reflect.DeepEqual(inputs["model"], update["model"]) && (driver == "" || t.chatProvider == driver) {
		return false
	}
	t.providerUpdates++
	if !t.providerVersion(ctx, "update", t.providerUpdates) || driver == "" {
		return false
	}
	t.chatProvider = driver
	t.providerPinCleanups = map[string]bool{}
	inputs[InputKeyProviderSwitch] = driver
	return true
}

// rememberPresetModel captures provenance BEFORE node args can replace the
// preset's selector with an exact ID. Resolution never mutates this value;
// it is not reconstructed from the concrete model's registry tags.
func rememberPresetModel(inputs map[string]interface{}) {
	if selector, ok := inputs["model"].(map[string]interface{}); ok && len(stringList(selector["tags"])) > 0 {
		copy := make(map[string]interface{}, len(selector))
		for k, v := range selector {
			copy[k] = v
		}
		inputs[inputKeyPresetModel] = copy
	}
}

func presetModelTags(inputs map[string]interface{}) []string {
	selector, _ := inputs[inputKeyPresetModel].(map[string]interface{})
	return stringList(selector["tags"])
}

// durableLookupKey marks the chat-wide stopped-agent lookup in flight among
// the tracker's stopped lookups, so the end-of-run check waits for it
// (stoppedAgentsAwaitingResume) as it waits for a doorbell's lookup.
const durableLookupKey = "__chat_resume_all"

// reviveDurableStopped brings back failed descendants an earlier execution
// stopped (and forgot), using the stopped-spawn lookup activity, and resumes
// every stopped descendant this execution holds. It is versioned at the call,
// not at startup, so an old run acts on its next live user action.
//
// It runs the lookup inline: for a run's start (ResumeInput.ResumeAllStopped),
// whose root waits on the agents it resumes before its first turn. A signal
// handler uses reviveDurableStoppedAsync instead.
func (t *ChildWorkflowTracker) reviveDurableStopped(ctx workflow.Context, chatID string) {
	if chatID == "" {
		return
	}
	t.durableResumes++
	if !t.providerVersion(ctx, "durable-resume", t.durableResumes) {
		return
	}
	participated := map[string]bool{}
	for _, rec := range t.liveDetachedSpawns {
		participated[rec.ChildThread] = true
	}
	t.beginStoppedLookup(durableLookupKey)
	defer t.endStoppedLookup(durableLookupKey)
	t.lookupAndResumeStopped(ctx, chatID, participated)
}

// reviveDurableStoppedAsync is reviveDurableStopped off the caller's
// coroutine, for the handlers of a user's message, Resume, or provider switch:
// the lookup (up to three attempts) must not hold up every signal queued behind
// it. Calls that arrive while a lookup runs coalesce into one more pass after
// it, never a second lookup at once, so a burst of messages costs at most two
// reads and resumes nothing twice. A failed read is logged and is retried by
// the user's next message, Resume or switch.
func (t *ChildWorkflowTracker) reviveDurableStoppedAsync(ctx workflow.Context, chatID string) {
	if workflow.GetVersion(ctx, providerSwitchChangeID+"/async-stopped-lookup", workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		t.reviveDurableStopped(ctx, chatID)
		return
	}
	if chatID == "" {
		return
	}
	t.durableResumes++
	if !t.providerVersion(ctx, "durable-resume", t.durableResumes) {
		return
	}
	if t.durableLookupRunning {
		t.durableLookupAgain = true
		return
	}
	t.durableLookupRunning = true
	// Snapshot participation before yielding to the database: a remembered
	// agent may finish its retry while the lookup is in flight. It must not
	// be retried a second time by this same recovery action.
	participated := map[string]bool{}
	for _, rec := range t.liveDetachedSpawns {
		participated[rec.ChildThread] = true
	}
	t.beginStoppedLookup(durableLookupKey)
	workflow.Go(ctx, func(gCtx workflow.Context) {
		defer func() {
			t.durableLookupRunning = false
			t.endStoppedLookup(durableLookupKey)
		}()
		for {
			t.durableLookupAgain = false
			t.lookupAndResumeStopped(gCtx, chatID, participated)
			if !t.durableLookupAgain || gCtx.Err() != nil {
				return
			}
		}
	})
}

// lookupAndResumeStopped reads the chat's stopped descendants from durable
// state, adopts the ones this execution does not know, and resumes every
// stopped descendant it holds, parents before children. One that cannot start
// because the run is ending is woken instead, so the run's successor resumes it.
func (t *ChildWorkflowTracker) lookupAndResumeStopped(ctx workflow.Context, chatID string, skip ...map[string]bool) {
	participated := map[string]bool{}
	if len(skip) > 0 {
		participated = skip[0]
	}
	var out struct {
		Agents []*db.StoppedSpawnAgent `json:"agents"`
	}
	if err := workflow.ExecuteActivity(markerContext(ctx), LookupStoppedSubAgentActivityName,
		map[string]interface{}{"chat_id": chatID, "all": true}).Get(ctx, &out); err != nil {
		workflow.GetLogger(ctx).Warn("Could not read stopped descendants for chat-wide resume", "error", err)
		return
	}
	for _, agent := range out.Agents {
		if agent == nil || participated[agent.ChildThreadID] || t.notStopped[agent.ChildThreadID] {
			continue
		}
		h := stoppedSpawnHandoff(agent)
		if t.adoptStoppedSpawn(&h) {
			// If the root is already ending, its successor must carry the
			// request rather than dropping the discovered agent.
			t.notifyThreadWake(h.ChildThread)
		}
	}
	for _, thread := range t.stoppedThreadsToWake(true) {
		if !participated[thread] && t.resumeStopped != nil {
			participated[thread] = true
			t.resumeStopped(ctx, thread)
		}
	}
}

// repeatedFailureReport reports whether a resumed sub-agent's failure is the
// report its parent already received from it (SpawnHandoff.LastFailure), so
// it is not delivered again. failure is the new report's digest, "" for an
// outcome that is not a failure. Versioned: a history recorded before it
// delivered every report.
func repeatedFailureReport(ctx workflow.Context, config *spawnChildWorkflowConfig, tracker *ChildWorkflowTracker, failure string) bool {
	if failure == "" || config == nil || !config.isResumption || tracker == nil {
		return false
	}
	rec := tracker.liveDetachedSpawns[config.toolCallID]
	if rec == nil || rec.handoff.LastFailure != failure {
		return false
	}
	return workflow.GetVersion(ctx, providerSwitchChangeID+"/repeat-failure-report", workflow.DefaultVersion, 1) != workflow.DefaultVersion
}

// failureReportDigest is db.FailureReportDigest: the identity a failure
// report's body is remembered by, in workflow state and in durable rows alike.
func failureReportDigest(body string) string { return db.FailureReportDigest(body) }

func isProviderSwitchHold(err error) bool {
	var app *temporal.ApplicationError
	return errors.As(err, &app) && app.Type() == "ProviderSwitchHold"
}

// A mapping error holds only the affected thread in place, with its own
// visible error card. No reset polling and no global pause: a provider/model
// edit, a message or Resume retries the same safe model call.
func (w *usageLimitWaiter) waitForProviderSwitch(ctx workflow.Context, req usageLimitWaitRequest, pc *PauseController) usageLimitOutcome {
	if workflow.GetVersion(ctx, providerSwitchChangeID+"/hold", workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return usageLimitOutcome{}
	}
	w.startListening()
	epoch, wakes, pauses := w.retryEpoch, w.tracker.threadWakeCount(req.Thread), w.pauser.pauseSignals
	_ = workflow.ExecuteActivity(markerContext(ctx), "WorkflowError", map[string]interface{}{
		"chat_id": w.chatID, "workflow_id": w.workflowID, "workflow_name": w.workflowName,
		"thread": req.Thread, "error_message": req.Err.Error(), "error_summary": req.Err.Error(),
		"error_id": "provider-switch-hold/" + w.chatID + "/" + req.Thread,
	}).Get(ctx, nil)
	err := workflow.Await(ctx, func() bool {
		return w.retryEpoch > epoch || w.tracker.threadWakeCount(req.Thread) > wakes ||
			w.pauser.pauseSignals > pauses || (req.Cancelled != nil && req.Cancelled())
	})
	if err != nil {
		return usageLimitOutcome{Handled: true, Err: err}
	}
	if req.Cancelled != nil && req.Cancelled() {
		return usageLimitOutcome{Handled: true, Err: ErrThreadCancelled}
	}
	pc.DoCheckPause(ctx)
	return usageLimitOutcome{Handled: true}
}
