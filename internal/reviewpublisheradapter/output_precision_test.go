package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
	historypb "go.temporal.io/api/history/v1"
)

func TestReviewOutputExpressionRejectsMatchingJSONFieldAliases(t *testing.T) {
	raw := reviewOutputYAML + "resume_node: review\n"
	graph := reviewGraphJSON(t, raw)
	require.NoError(t, validateReviewOutputExpressions([]byte(raw), graph, true))
	// Each spelling alone describes precisely the YAML graph. With both present,
	// refusal must be strict duplicate-field decoding, not proto inequality.
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(graph, &fields))
	require.Contains(t, fields, "resumeNode")
	fields["resume_node"] = fields["resumeNode"]
	delete(fields, "resumeNode")
	snakeOnly, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, validateReviewOutputExpressions([]byte(raw), snakeOnly, true))
	fields["resumeNode"] = fields["resume_node"]
	ambiguous, err := json.Marshal(fields)
	require.NoError(t, err)
	require.ErrorIs(t, validateReviewOutputExpressions([]byte(raw), ambiguous, true), reviewpublisher.ErrUnauthorized)
}

func TestHistoryOutputExpressionRejectsNumericPrecisionCollision(t *testing.T) {
	raw := reviewOutputYAML + "inputs:\n  limit:\n    type: integer\n    default: 9007199254740992\n"
	// Use unquoted int64 JSON to expose the generic float64 canonicalizer's
	// collision while remaining valid strict protobuf JSON.
	graph := []byte(strings.ReplaceAll(string(reviewGraphJSON(t, raw)), `"9007199254740992"`, "9007199254740992"))
	changed := []byte(strings.ReplaceAll(string(graph), "9007199254740992", "9007199254740993"))
	require.NotEqual(t, graph, changed)
	require.True(t, sameJSON(graph, changed), "fixture must reach the generic canonicalizer collision")
	require.NoError(t, validateReviewOutputExpressions([]byte(raw), graph, true))
	require.ErrorIs(t, validateReviewOutputExpressions([]byte(raw), changed, true), reviewpublisher.ErrUnauthorized)

	for _, changedPolicy := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed recorded graph", true: "changed policy graph"}[changedPolicy], func(t *testing.T) {
			r, a, f := evidenceFixture(t)
			var snapshot GraphSnapshot
			require.NoError(t, json.Unmarshal(a.GraphSnapshot, &snapshot))
			snapshot.YAML["trusted"] = []byte(raw)
			snapshot.Graphs["trusted"] = graph
			recorded := changed
			if changedPolicy {
				snapshot.Graphs["trusted"] = changed
				recorded = graph
			}
			a.GraphSnapshot, _ = json.Marshal(snapshot)
			a.Binding.WorkflowDigest, _ = store.DigestReviewSnapshot(a.GraphSnapshot)
			f.events[2].GetActivityTaskCompletedEventAttributes().Result = payload(t, map[string]any{"yaml": []byte(raw), "workflow_json": recorded, "has_templates": true})
			_, err := r.Verify(context.Background(), a, "review-run", "private-review-completion", 4, true)
			require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
		})
	}
}

func TestHistoryAcceptsPinnedOutputExpressionDependency(t *testing.T) {
	r, a, f := evidenceFixture(t)
	raw := strings.Replace(reviewOutputYAML, "name: trusted", "name: dependency", 1)
	graph := reviewGraphJSON(t, raw)
	var snapshot GraphSnapshot
	require.NoError(t, json.Unmarshal(a.GraphSnapshot, &snapshot))
	snapshot.Graphs["dependency"] = graph
	snapshot.YAML["dependency"] = []byte(raw)
	a.GraphSnapshot, _ = json.Marshal(snapshot)
	a.Binding.WorkflowDigest, _ = store.DigestReviewSnapshot(a.GraphSnapshot)
	// Add an actually observed dependency load before reserved completion;
	// root and dependency must both remain pinned and account for all graphs.
	scheduled := &historypb.HistoryEvent{}
	completed := &historypb.HistoryEvent{}
	// Independent messages avoid mutating the root load while making a second.
	rootSchedule := f.events[1].GetActivityTaskScheduledEventAttributes()
	scheduled.EventId = 4
	scheduled.EventType = f.events[1].EventType
	scheduled.Attributes = &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{ActivityId: "load-dependency", ActivityType: rootSchedule.ActivityType, Input: payload(t, map[string]string{"workflow_name": "dependency"})}}
	completed.EventId = 5
	completed.EventType = f.events[2].EventType
	completed.Attributes = &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{ScheduledEventId: 4, Result: payload(t, map[string]any{"yaml": []byte(raw), "workflow_json": graph, "has_templates": true})}}
	for _, event := range f.events[3:] {
		event.EventId += 2
		if attributes := event.GetActivityTaskCompletedEventAttributes(); attributes != nil {
			attributes.ScheduledEventId += 2
		}
	}
	f.events = append(f.events[:3:3], append([]*historypb.HistoryEvent{scheduled, completed}, f.events[3:]...)...)
	evidence, err := r.Verify(context.Background(), a, "review-run", "private-review-completion", 6, true)
	require.NoError(t, err)
	require.True(t, evidence.GraphSucceeded)
	require.True(t, evidence.Terminal)
	require.Equal(t, reviewpublisher.Approve, evidence.Verdict)
}
