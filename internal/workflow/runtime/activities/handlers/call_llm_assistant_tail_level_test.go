// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// yieldLine is one line a recordingYieldLogger was given.
type yieldLine struct {
	msg    string
	fields map[string]any
}

// recordingYieldLogger records which level each line was logged at, with its
// fields.
type recordingYieldLogger struct {
	infos, errors []yieldLine
}

func record(msg string, keyvals []interface{}) yieldLine {
	line := yieldLine{msg: msg, fields: map[string]any{}}
	for i := 0; i+1 < len(keyvals); i += 2 {
		if key, ok := keyvals[i].(string); ok {
			line.fields[key] = keyvals[i+1]
		}
	}
	return line
}

func (l *recordingYieldLogger) Info(msg string, keyvals ...interface{}) {
	l.infos = append(l.infos, record(msg, keyvals))
}

func (l *recordingYieldLogger) Error(msg string, keyvals ...interface{}) {
	l.errors = append(l.errors, record(msg, keyvals))
}

func assistantTail(parts ...message.ContentPart) message.Message {
	return message.Message{ID: "msg", Role: message.Assistant, Parts: parts}
}

// The assistant-tail yield is a fault only on a turn that was promised new
// input. Chat 9fbaace8 (Sentry ELECTRON-CM) reached it on a turn the loop-exit
// gate had taken for a background spawn's report the previous turn had
// already delivered, and paged as an upstream bug: the gate re-enters on
// counters, so on its turns finding nothing new is the expected answer.
func TestAssistantTailYield_SpeculativeTurnIsNotAFault(t *testing.T) {
	logger := &recordingYieldLogger{}

	logAssistantTailYield(logger, true, "chat", "thread", assistantTail(message.TextContent{Text: "done"}), 255)

	assert.Empty(t, logger.errors,
		"a turn the loop took on a hint was promised nothing; finding nothing new is not an error")
	require.Len(t, logger.infos, 1, "the yield is still logged, so the turn that took it is visible")
}

// Every other turn was promised input — a tool result, or a mailbox row the
// end-of-turn probe saw — so an assistant tail still means something upstream
// sent the loop round with nothing to answer, and it stays an ERROR under the
// message Sentry already groups it by. GO-1E: three such events after the
// speculative split (#713), two on chat 9fbaace8 again — a real fault, so it
// is not downgraded.
func TestAssistantTailYield_PromisedTurnIsStillAFault(t *testing.T) {
	logger := &recordingYieldLogger{}

	logAssistantTailYield(logger, false, "chat", "thread", assistantTail(message.TextContent{Text: "done"}), 255)

	assert.Empty(t, logger.infos)
	require.Len(t, logger.errors, 1)
	assert.Contains(t, logger.errors[0].msg,
		"[CallLLM] History ends with an assistant message after the mailbox drain",
		"the fault keeps its wording, so real occurrences stay one Sentry issue")
}

// The fault's event must say enough to tell its causes apart without a
// database query. GO-1E's events carried the chat and message ids but not the
// thread — the key was "thread", which is not an id key, so it never became a
// Sentry tag, and a spawned thread runs inline in its parent's execution, so
// WorkflowID does not name it either.
func TestAssistantTailYield_FaultCarriesThreadAndTailShape(t *testing.T) {
	for name, tc := range map[string]struct {
		tail      message.Message
		toolCalls int
		hasText   bool
	}{
		"text reply":            {tail: assistantTail(message.TextContent{Text: "all done"}), hasText: true},
		"tool results missing":  {tail: assistantTail(message.ToolCall{ID: "tc-1", Name: "shell"}, message.ToolCall{ID: "tc-2", Name: "view"}), toolCalls: 2},
		"empty or interrupted":  {tail: assistantTail(message.TextContent{Text: "  "})},
		"text with a tool call": {tail: assistantTail(message.TextContent{Text: "running it"}, message.ToolCall{ID: "tc-1", Name: "shell"}), toolCalls: 1, hasText: true},
	} {
		t.Run(name, func(t *testing.T) {
			logger := &recordingYieldLogger{}

			logAssistantTailYield(logger, false, "chat-1", "thread-1", tc.tail, 82)

			require.Len(t, logger.errors, 1)
			fields := logger.errors[0].fields
			assert.Equal(t, "chat-1", fields["chatID"])
			assert.Equal(t, "thread-1", fields["threadID"], "an id key, so the thread becomes a Sentry tag")
			assert.Equal(t, "msg", fields["lastMsgID"])
			assert.Equal(t, 82, fields["msgCount"])
			assert.Equal(t, tc.toolCalls, fields["tailToolCalls"])
			assert.Equal(t, tc.hasText, fields["tailHasText"])
		})
	}
}
