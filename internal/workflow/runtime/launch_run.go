// Copyright (c) 2025 Reliant Labs
package runtime

// The launch run: the one run of a chat that whatever launched the chat
// started.
//
// A chat's launch kind (chat.start, schedule, agent.start_run, ...) names who
// started its FIRST run, and nothing after it. A chat outlives that run: every
// reply a person sends starts a new run of the same root workflow, in a chat a
// schedule, a webhook or an agent may have launched. Policy that depends on who
// is waiting — whether a finish notifies, whether a failure joins a trigger's
// streak — must ask about the run, so the launcher marks the run it starts and
// nothing else does. A run without the mark was started by a person replying.
//
// It is a RuntimeInjectedInput, so BuildWorkflowInputs refuses it from a
// client, and continue-as-new carries it with the rest of the inputs.
const InputKeyLaunchRun = "__launch_run"

// IsLaunchRun reports whether this run is the chat's launch run.
func IsLaunchRun(inputs map[string]interface{}) bool {
	launchRun, _ := inputs[InputKeyLaunchRun].(bool)
	return launchRun
}
