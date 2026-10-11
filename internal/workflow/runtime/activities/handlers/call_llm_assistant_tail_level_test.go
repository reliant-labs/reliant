// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingYieldLogger records which level each line was logged at.
type recordingYieldLogger struct {
	infos, errors []string
}

func (l *recordingYieldLogger) Info(msg string, _ ...interface{})  { l.infos = append(l.infos, msg) }
func (l *recordingYieldLogger) Error(msg string, _ ...interface{}) { l.errors = append(l.errors, msg) }

// The assistant-tail yield is a fault only on a turn that was promised new
// input. Chat 9fbaace8 (Sentry ELECTRON-CM) reached it on a turn the loop-exit
// gate had taken for a background spawn's report the previous turn had
// already delivered, and paged as an upstream bug: the gate re-enters on
// counters, so on its turns finding nothing new is the expected answer.
func TestAssistantTailYield_SpeculativeTurnIsNotAFault(t *testing.T) {
	logger := &recordingYieldLogger{}

	logAssistantTailYield(logger, true, "chat", "thread", "msg", 255)

	assert.Empty(t, logger.errors,
		"a turn the loop took on a hint was promised nothing; finding nothing new is not an error")
	require.Len(t, logger.infos, 1, "the yield is still logged, so the turn that took it is visible")
}

// Every other turn was promised input — a tool result, or a mailbox row the
// end-of-turn probe saw — so an assistant tail still means something upstream
// sent the loop round with nothing to answer, and it stays an ERROR under the
// message Sentry already groups it by.
func TestAssistantTailYield_PromisedTurnIsStillAFault(t *testing.T) {
	logger := &recordingYieldLogger{}

	logAssistantTailYield(logger, false, "chat", "thread", "msg", 255)

	assert.Empty(t, logger.infos)
	require.Len(t, logger.errors, 1)
	assert.Contains(t, logger.errors[0],
		"[CallLLM] History ends with an assistant message after the mailbox drain",
		"the fault keeps its wording, so real occurrences stay one Sentry issue")
}
