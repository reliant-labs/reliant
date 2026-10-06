// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// An activity's helper goroutines (the worker-stop watcher and the heartbeat)
// belong to that one execution: Execute must not return while either is still
// running. One that outlives Execute outlives the test that ran it too, and
// keeps running wrapper code while a later test changes what that code reads.
func TestActivityWrapper_HelperGoroutinesDoNotOutliveExecute(t *testing.T) {
	t.Parallel()
	h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		return thinkingCallLLMOutput(), nil
	})

	// The test activity environment runs Execute on the calling goroutine,
	// so everything Execute starts is "created by ... in goroutine <self>".
	// Matching on that keeps parallel tests' wrappers out of the count.
	self := currentGoroutineID(t)

	// Several runs: a helper that has been cancelled but not yet scheduled
	// shows up on some returns and not others.
	for run := 0; run < 20; run++ {
		_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
		require.NoError(t, err)
		require.Empty(t, goroutinesStartedByWrapperIn(self),
			"run %d: a goroutine started by ActivityWrapper.Execute is still running after it returned", run)
	}
}

// currentGoroutineID returns the calling goroutine's id, as the traceback
// prints it.
func currentGoroutineID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 64)
	buf = buf[:goruntime.Stack(buf, false)]
	// "goroutine 123 [running]:"
	fields := strings.Fields(string(buf))
	require.True(t, len(fields) >= 2 && fields[0] == "goroutine", "unexpected traceback header %q", buf)
	return fields[1]
}

// goroutinesStartedByWrapperIn returns the stack of every live goroutine that
// ActivityWrapper.Execute started while running on goroutine creatorID.
func goroutinesStartedByWrapperIn(creatorID string) []string {
	buf := make([]byte, 1<<20)
	for {
		n := goruntime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var started []string
	for _, stack := range strings.Split(string(buf), "\n\n") {
		for _, line := range strings.Split(stack, "\n") {
			if strings.HasPrefix(line, "created by ") &&
				strings.Contains(line, "/internal/workflow/runtime.(*ActivityWrapper[") &&
				strings.HasSuffix(line, " in goroutine "+creatorID) {
				started = append(started, stack)
				break
			}
		}
	}
	return started
}
