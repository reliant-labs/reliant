// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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

// repeatedToolErrorInputChangeID gates keying the breaker on each call's
// input as well as its tool and error (reliant#2752). Three DIFFERENT missing
// paths answered "not found" read as one failure repeated, and ended an agent
// that was exploring. A history recorded before it tripped on the looser
// signature, so replaying it must still trip there.
const repeatedToolErrorInputChangeID = "repeated-tool-error-input"

// RepeatedToolErrorBreaker counts, per thread, consecutive execute_tools
// batches in which EVERY tool result failed and the batch's failures are the
// same as the previous batch's, and reports when the streak reaches the limit.
//
// Counting policy:
//   - A batch whose results are all errors, identical (tool, input and
//     message) to the previous failing batch → streak++. The same error for a
//     different input is exploration, not a retry loop: a new streak.
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
	// legacy is the same count keyed without the inputs, as the breaker
	// counted before repeatedToolErrorInputChangeID: a history recorded then
	// trips where it did.
	legacy map[string]*toolErrorStreak
}

type toolErrorStreak struct {
	signature string
	count     int
}

// NewRepeatedToolErrorBreaker creates a breaker that trips at limit.
func NewRepeatedToolErrorBreaker(limit int) *RepeatedToolErrorBreaker {
	return &RepeatedToolErrorBreaker{limit: limit, streaks: map[string]*toolErrorStreak{}, legacy: map[string]*toolErrorStreak{}}
}

// Observe records a completed step for thread and returns an error once the
// thread's streak of identical failing batches reaches the limit, else nil.
//
// inputs maps each tool call id of the batch to its input, so a batch that
// fails the same way for a different input is not a repeat. inputAware
// decides, only when the input-blind count would trip and the input-aware one
// would not, which one this run follows (a workflow.GetVersion in the step
// executor); nil means input-aware. Nil-receiver safe.
func (b *RepeatedToolErrorBreaker) Observe(thread, activityName string, stepEvent *StepEvent, inputs map[string]string, inputAware func() bool) error {
	if b == nil || activityName != executeToolsActivityName || stepEvent == nil || stepEvent.Error != nil {
		return nil
	}
	results, ok := stepEvent.Data["tool_results"].([]interface{})
	if !ok || len(results) == 0 {
		return nil
	}
	signature, allFailed := failureSignature(results, inputs)
	legacySignature, _ := failureSignature(results, nil)
	if !allFailed {
		delete(b.streaks, thread)
		delete(b.legacy, thread)
		return nil
	}
	count := bump(b.streaks, thread, signature)
	legacyCount := bump(b.legacy, thread, legacySignature)
	switch {
	case count >= b.limit:
	case legacyCount >= b.limit && inputAware != nil && !inputAware():
		count = legacyCount
	default:
		return nil
	}
	return &TerminalError{Message: fmt.Sprintf(
		"the agent's tool calls failed %d times in a row with the same error, so the run was ended instead of retrying again: %s",
		count, describeFailures(results))}
}

// bump extends thread's streak in streaks when signature repeats it, or starts
// a new one, and returns its length.
func bump(streaks map[string]*toolErrorStreak, thread, signature string) int {
	streak := streaks[thread]
	if streak == nil || streak.signature != signature {
		streak = &toolErrorStreak{signature: signature}
		streaks[thread] = streak
	}
	streak.count++
	return streak.count
}

// inputDigest is a short, stable stand-in for a tool call's input in a
// failure signature: the input itself can be large.
func inputDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:8])
}

// failureSignature identifies a batch by its failures — each result's tool,
// the digest of the input it was called with (when inputs has it), and its
// error — and reports whether every result in it failed.
func failureSignature(results []interface{}, inputs map[string]string) (string, bool) {
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
		input := ""
		if id, _ := tr["tool_call_id"].(string); id != "" {
			if in, ok := inputs[id]; ok {
				input = inputDigest(in)
			}
		}
		parts = append(parts, name+"\x00"+input+"\x00"+content)
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

// toolCallInputs maps each resolved tool call of an execute_tools node to its
// input, or nil for any other node.
func toolCallInputs(node *reliantv1.Node) map[string]string {
	calls := node.GetExecuteTools().GetResolvedToolCalls()
	if len(calls) == 0 {
		return nil
	}
	inputs := make(map[string]string, len(calls))
	for _, tc := range calls {
		inputs[tc.GetId()] = tc.GetInput()
	}
	return inputs
}
