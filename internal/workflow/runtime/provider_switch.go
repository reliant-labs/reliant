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

// selectedProvider is the one provider explicitly named by the root model.
// Multi-provider preferences and tag-only picks are not a chat-wide switch to
// one provider. The handler supplies a resolved driver for those picks.
func selectedProvider(inputs map[string]interface{}) string {
	if driver, _ := inputs[InputKeyProviderSwitch].(string); driver != "" {
		return driver
	}
	if own := explicitModelProviders(inputs["model"]); len(own) == 1 {
		return own[0]
	}
	return ""
}

// applyProviderSwitch changes routing, not the descendants' semantic model
// selectors. All executors share the tracker, so active, nested and future
// spawns see the driver at the next model call. Inputs carry it across a
// continue-as-new or fresh resumption. No activity/tool is cancelled.
func (t *ChildWorkflowTracker) applyProviderSwitch(ctx workflow.Context, inputs map[string]interface{}, update map[string]interface{}) bool {
	model, changed := update["model"]
	if !changed {
		return false
	}
	// Repeated composer params are not a new provider choice. In particular
	// a normal message must not reset routing leases or retry stopped agents
	// merely because it repeats the root's selector.
	if reflect.DeepEqual(inputs["model"], model) && (selectedProvider(update) == "" || t.chatProvider == selectedProvider(update)) {
		return false
	}
	t.providerUpdates++
	if workflow.GetVersion(ctx, fmt.Sprintf("%s/update/%d", providerSwitchChangeID, t.providerUpdates), workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return false
	}
	driver := selectedProvider(update)
	if driver == "" {
		return false
	}
	t.chatProvider = driver
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

// reviveDurableStopped brings back failed descendants forgotten by an earlier
// execution, using the existing stopped-spawn lookup activity. It is versioned
// at the wake, not startup, so an old run can act on its next live user action.
func (t *ChildWorkflowTracker) reviveDurableStopped(ctx workflow.Context, chatID string) {
	t.durableResumes++
	if workflow.GetVersion(ctx, fmt.Sprintf("%s/durable-resume/%d", providerSwitchChangeID, t.durableResumes), workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return
	}
	// End-of-run waits for durable lookup work just as it does for a
	// message's single-thread lookup. Otherwise the root can finish while
	// this activity is reading the agents it is supposed to wait for.
	lookupKey := fmt.Sprintf("__chat_resume_all/%d", t.durableResumes)
	t.beginStoppedLookup(lookupKey)
	defer t.endStoppedLookup(lookupKey)
	var out struct {
		Agents []*db.StoppedSpawnAgent `json:"agents"`
	}
	if err := workflow.ExecuteActivity(markerContext(ctx), LookupStoppedSubAgentActivityName,
		map[string]interface{}{"chat_id": chatID, "all": true}).Get(ctx, &out); err != nil {
		workflow.GetLogger(ctx).Warn("Could not read stopped descendants for chat-wide resume", "error", err)
		return
	}
	for _, agent := range out.Agents {
		if t.notStopped[agent.ChildThreadID] {
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
		if t.resumeStopped != nil {
			t.resumeStopped(ctx, thread)
		}
	}
}

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
