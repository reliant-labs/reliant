// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"sort"
	"strings"
)

// RepeatedToolErrorLimit is how many consecutive tool batches that fail with
// the very same error an unattended agent may produce before its step is ended
// with an error. Retrying a failure that did not change is not progress: the
// model is told the same thing every turn and answers the same way, so the
// loop only ends at max_turns — about 80 messages and tens of thousands of
// tokens per cycle — with nobody watching.
const RepeatedToolErrorLimit = 3

// repeatedToolErrorChangeID gates the breaker (workflow.GetVersion): a history
// recorded before it kept looping through identical failures, and replaying it
// with the breaker would end those steps early, a different command sequence
// than the one recorded.
const repeatedToolErrorChangeID = "repeated-tool-error-breaker"

// RepeatedToolErrorBreaker counts, per thread, consecutive execute_tools
// batches in which EVERY tool result failed and the batch's failures are the
// same as the previous batch's, and reports when the streak reaches the limit.
//
// Counting policy:
//   - A batch whose results are all errors, identical (tool and message) to the
//     previous failing batch → streak++.
//   - A batch with a different set of errors starts a new streak at 1.
//   - A batch where any tool succeeded → streak cleared: the agent is getting
//     somewhere.
//   - Anything else (call_llm, save_message, a batch with no tool results) is
//     neutral: an agent loop interleaves model turns between tool batches, and
//     resetting on them would mean the streak never forms.
//
// Lives on the workflow's deterministic stack, is never persisted, and is fed
// only recorded activity results, so replay rebuilds it exactly. The streak is
// keyed by thread so a parent and the sub-agents it spawned do not mix.
type RepeatedToolErrorBreaker struct {
	limit   int
	streaks map[string]*toolErrorStreak
}

type toolErrorStreak struct {
	signature string
	count     int
}

// NewRepeatedToolErrorBreaker creates a breaker that trips at limit.
func NewRepeatedToolErrorBreaker(limit int) *RepeatedToolErrorBreaker {
	return &RepeatedToolErrorBreaker{limit: limit, streaks: map[string]*toolErrorStreak{}}
}

// Observe records a completed step for thread and returns an error once the
// thread's streak of identical failing batches reaches the limit, else nil.
// Nil-receiver safe.
func (b *RepeatedToolErrorBreaker) Observe(thread, activityName string, stepEvent *StepEvent) error {
	if b == nil || activityName != executeToolsActivityName || stepEvent == nil || stepEvent.Error != nil {
		return nil
	}
	results, ok := stepEvent.Data["tool_results"].([]interface{})
	if !ok || len(results) == 0 {
		return nil
	}
	signature, allFailed := failureSignature(results)
	if !allFailed {
		delete(b.streaks, thread)
		return nil
	}
	streak := b.streaks[thread]
	if streak == nil || streak.signature != signature {
		streak = &toolErrorStreak{signature: signature}
		b.streaks[thread] = streak
	}
	streak.count++
	if streak.count < b.limit {
		return nil
	}
	return &TerminalError{Message: fmt.Sprintf(
		"the agent's tool calls failed %d times in a row with the same error, so the run was ended instead of retrying again: %s",
		streak.count, describeFailures(results))}
}

// failureSignature identifies a batch by its failures, and reports whether
// every result in it failed.
func failureSignature(results []interface{}) (string, bool) {
	parts := make([]string, 0, len(results))
	for _, item := range results {
		tr, ok := item.(map[string]interface{})
		if !ok {
			return "", false
		}
		if isErr, _ := tr["is_error"].(bool); !isErr {
			return "", false
		}
		name, _ := tr["name"].(string)
		content, _ := tr["content"].(string)
		parts = append(parts, name+"\x00"+content)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01"), true
}

func describeFailures(results []interface{}) string {
	var out []string
	for _, item := range results {
		tr, _ := item.(map[string]interface{})
		name, _ := tr["name"].(string)
		content, _ := tr["content"].(string)
		if len(content) > 300 {
			content = content[:300] + "…"
		}
		out = append(out, fmt.Sprintf("%s: %s", name, content))
	}
	return strings.Join(out, "; ")
}
