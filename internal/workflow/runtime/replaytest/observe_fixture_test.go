// Copyright (c) 2026 Reliant Labs

package replaytest

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The two workflow_observe histories pin the observe-workflow-nodes gate from
// both sides only while each still has the shape it claims: a regeneration
// that lost the recordings (or a frozen set that somehow had them) would keep
// replaying and prove nothing. TestReplayFixtures checks they replay; this
// checks what they replay.
func TestWorkflowObserveFixturesPinBothShapes(t *testing.T) {
	t.Parallel()
	current := recordNodeObservationMarkers(t, filepath.Join("fixtures", "workflow_observe.json"))
	// plan, probe, the join, two ticks, the loop, the workflow node.
	require.Equal(t, 7, current, "the current history records every observed node the workflow completes itself")

	frozen := recordNodeObservationMarkers(t, filepath.Join("fixtures", "frozen", "2026-11-24-observe-unrecorded", "workflow_observe.json"))
	require.Zero(t, frozen, "the frozen history was recorded before the workflow recorded these nodes")
}

// recordNodeObservationMarkers counts the RecordNodeObservation local-activity
// markers in a history fixture.
func recordNodeObservationMarkers(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var history struct {
		Events []struct {
			EventType  string `json:"eventType"`
			Attributes struct {
				MarkerName string `json:"markerName"`
				Details    map[string]struct {
					Payloads []struct {
						Data string `json:"data"`
					} `json:"payloads"`
				} `json:"details"`
			} `json:"markerRecordedEventAttributes"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &history))
	require.NotEmpty(t, history.Events, "%s holds no events", path)
	n := 0
	for _, ev := range history.Events {
		if ev.Attributes.MarkerName != "LocalActivity" {
			continue
		}
		for _, p := range ev.Attributes.Details["data"].Payloads {
			data, err := base64.StdEncoding.DecodeString(p.Data)
			require.NoError(t, err)
			if strings.Contains(string(data), `"ActivityType":"RecordNodeObservation"`) {
				n++
			}
		}
	}
	return n
}
