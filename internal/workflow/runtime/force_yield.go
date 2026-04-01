// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
)

// ErrForceYielded is a sentinel error returned by executors when a force-yield
// signal has been received for their thread. The parent workflow handles this
// by treating the sub-workflow as completed with a yield message.
var ErrForceYielded = errors.New("force yielded: the user requested a yield before the sub-workflow completed")

// forceYieldMessage builds the yield message for a given thread.
// NOTE: This message is used as response_text in the node output. For spawned
// threads, the ResultMapper in step_executor.go prepends the <system>agent_id</system>
// tag, so we must NOT include it here to avoid duplication.
func forceYieldMessage(threadID string) string {
	return fmt.Sprintf(
		"The user requested this thread to yield before work was fully completed. "+
			"You can resume it later with agent_id: %s",
		threadID,
	)
}

// forceYieldNodeOutput builds the output map used when a workflow or loop node
// is force-yielded. This is the single source of truth for the yield output
// shape consumed by save_message, edge routing, and parent CEL expressions.
func forceYieldNodeOutput(threadID string) map[string]interface{} {
	msg := forceYieldMessage(threadID)
	return map[string]interface{}{
		"message":       msg,
		"response_text": msg,
		"yielded":       true,
		"thread":        threadID,
	}
}
