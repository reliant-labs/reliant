// Copyright (c) 2025 Reliant Labs
package replaytest

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureHistory is the slice of a protojson History this file reads.
type fixtureHistory struct {
	Events []struct {
		EventType                               string `json:"eventType"`
		WorkflowExecutionStartedEventAttributes *struct {
			Input struct {
				Payloads []struct {
					Data string `json:"data"`
				} `json:"payloads"`
			} `json:"input"`
		} `json:"workflowExecutionStartedEventAttributes"`
		ActivityTaskScheduledEventAttributes *struct {
			ActivityType struct {
				Name string `json:"name"`
			} `json:"activityType"`
		} `json:"activityTaskScheduledEventAttributes"`
	} `json:"events"`
}

// TestReplayFixtures only proves a fixture replays; it cannot tell a fixture
// that pins the probe from one that silently stopped scheduling it. This pins
// what greenfield_probe.json is for: a first-turn run whose recorded start
// asks for the probe, and which schedules it before its first LLM call.
func TestGreenfieldProbeFixturePinsTheProbeBeforeTheFirstLLMCall(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("fixtures", "greenfield_probe.json"))
	require.NoError(t, err)
	var history fixtureHistory
	require.NoError(t, json.Unmarshal(raw, &history))
	require.NotEmpty(t, history.Events)

	started := history.Events[0].WorkflowExecutionStartedEventAttributes
	require.NotNil(t, started)
	require.NotEmpty(t, started.Input.Payloads)
	data, err := base64.StdEncoding.DecodeString(started.Input.Payloads[0].Data)
	require.NoError(t, err)
	var input struct {
		GreenfieldProbe bool
	}
	require.NoError(t, json.Unmarshal(data, &input))
	assert.True(t, input.GreenfieldProbe, "the recorded start must ask for the probe")

	var scheduled []string
	for _, event := range history.Events {
		if event.ActivityTaskScheduledEventAttributes != nil {
			scheduled = append(scheduled, event.ActivityTaskScheduledEventAttributes.ActivityType.Name)
		}
	}
	probeAt, llmAt := -1, -1
	for i, name := range scheduled {
		if name == "GreenfieldProbe" && probeAt == -1 {
			probeAt = i
		}
		if name == "CallLLM" && llmAt == -1 {
			llmAt = i
		}
	}
	require.NotEqual(t, -1, probeAt, "scheduled: %v", scheduled)
	require.NotEqual(t, -1, llmAt, "scheduled: %v", scheduled)
	assert.Less(t, probeAt, llmAt, "scheduled: %v", scheduled)
}
