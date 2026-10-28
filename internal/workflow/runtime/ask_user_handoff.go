package runtime

// PendingToolBatch is an agent-loop iteration stopped inside its execute_tools
// batch, waiting on a blocking ask_user, when a continue-as-new handoff took
// the spawn. See ask_user_handoff.go.
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
