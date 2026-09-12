// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Three separate executors decide what happens to a retry-exhausted step, and
// they must decide it the same way: DynamicWorkflow (workflow.go),
// InlineLoopExecutor (loop_executor.go) and InlineWorkflowExecutor
// (inline_workflow_executor.go). A plain agent loop runs through one, a loop
// node through another, a nested workflow node through the third — so a fix
// applied to only one produces a chat that survives a heartbeat burst at the
// top level and dies inside a sub-workflow, which is indistinguishable from
// flakiness.
//
// That has happened before in exactly these files: see the comment in
// inline_workflow_executor.go about pause handling being added to the loop
// executor but not the inline one, "which is why a plain agent loop paused
// correctly and only nested workflow nodes regressed."
//
// This is a source check rather than a behavioral one because reaching the
// three sites for real needs a workflow environment, a chat, a provider and a
// database each. It cannot prove they behave identically; it proves nobody
// removed the guard from one of them, which is the failure that actually
// happens.
func TestRetryExhaustionSitesShareOneDecision(t *testing.T) {
	t.Parallel()

	sites := []struct {
		file   string
		execur string
	}{
		{"workflow.go", "DynamicWorkflow"},
		{"loop_executor.go", "InlineLoopExecutor"},
		{"inline_workflow_executor.go", "InlineWorkflowExecutor"},
	}

	for _, site := range sites {
		t.Run(site.file, func(t *testing.T) {
			src, err := os.ReadFile(site.file)
			require.NoError(t, err)
			text := string(src)

			require.Contains(t, text, "heartbeatCancelExhausted(stepEvent.Error)",
				"%s (%s) must classify a heartbeat-exhausted ladder before pausing; "+
					"without it a heartbeat burst pauses the chat with an error the user cannot act on",
				site.file, site.execur)

			require.Contains(t, text, "grantRestart(running.StepID)",
				"%s (%s) must bound its re-dispatches; an unbounded restart livelocks "+
					"the chat when Temporal is genuinely down", site.file, site.execur)

			require.True(t, strings.Contains(text, "heartbeatRestarts.clear("),
				"%s (%s) must clear a step's allowance once it succeeds, or a long chat "+
					"inherits an allowance spent hours earlier", site.file, site.execur)
		})
	}
}
