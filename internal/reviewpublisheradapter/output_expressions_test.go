package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
)

const reviewOutputYAML = "name: trusted\nentry: [review]\nnodes:\n  - id: review\n    type: call_llm\n    outcome: success\noutputs:\n  review_verdict: '{{ nodes.review.response_text }}'\n"

func reviewGraphJSON(t *testing.T, raw string) []byte {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(raw))
	require.NoError(t, err)
	graph, err := protojson.Marshal(wf)
	require.NoError(t, err)
	return graph
}

func TestReviewOutputExpressionStructure(t *testing.T) {
	graph := reviewGraphJSON(t, reviewOutputYAML)
	require.NoError(t, validateReviewOutputExpressions([]byte(reviewOutputYAML), graph, true))
	literal := strings.ReplaceAll(reviewOutputYAML, "{{ nodes.review.response_text }}", "approve")
	require.NoError(t, validateReviewOutputExpressions([]byte(literal), reviewGraphJSON(t, literal), false))
	// HasTemplates may be true because the real loader inspected source comments
	// before normalizing YAML; it is not redefined to mean load-time evaluation.
	require.NoError(t, validateReviewOutputExpressions([]byte(literal), reviewGraphJSON(t, literal), true))

	for name, raw := range map[string]string{
		"input-dependent name": strings.Replace(reviewOutputYAML, "name: trusted", "name: '{{ inputs.name }}'", 1),
		"node arg":             strings.Replace(reviewOutputYAML, "    outcome: success", "    outcome: success\n    args:\n      model: '{{ inputs.model }}'", 1),
		"node identity":        strings.Replace(reviewOutputYAML, "id: review", "id: '{{ inputs.node }}'", 1),
		"dependency ref":       "name: trusted\nnodes:\n  - id: sub\n    type: workflow\n    ref: '{{ inputs.dependency }}'\n",
		"nested output":        "name: trusted\non_failure:\n  name: failed\n  outputs:\n    verdict: '{{ failure.message }}'\n",
		"trigger input":        "name: trusted\ntriggers:\n  - name: event\n    inputs:\n      value: '{{ trigger.payload }}'\n",
		"output key":           "name: trusted\noutputs:\n  '{{ inputs.key }}': approve\n",
		"non-string output":    "name: trusted\noutputs:\n  review_verdict: true\n",
		"nested output object": "name: trusted\noutputs:\n  review_verdict: {value: '{{ nodes.review.response_text }}'}\n",
		"duplicate root key":   reviewOutputYAML + "name: trusted\n",
		"duplicate nested key": "name: trusted\nnodes:\n  - id: review\n    id: shadow\n    type: call_llm\n",
		"duplicate output":     reviewOutputYAML + "  review_verdict: approve\n",
		"alias":                "name: &name trusted\noutputs:\n  verdict: *name\n",
		"merge":                "name: trusted\n<<: {name: other}\n",
		"custom tag":           "name: !custom trusted\n",
		"non-string key":       "name: trusted\n1: value\n",
		"multiple documents":   reviewOutputYAML + "---\nname: other\n",
		"malformed":            "name: [unterminated\n",
		"field aliases":        "name: trusted\nresumeNode: a\nresume_node: b\n",
		"empty":                "",
		"overdepth":            "name: trusted\nunknown: " + strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := graph
			// When the general parser accepts the shape, supply its matching
			// JSON so denial must come from the private structural restriction,
			// not an unrelated correspondence mismatch.
			if parsed, err := wfyaml.ParseWorkflow([]byte(raw)); err == nil {
				candidate, err = protojson.Marshal(parsed)
				require.NoError(t, err)
			}
			require.ErrorIs(t, validateReviewOutputExpressions([]byte(raw), candidate, true), reviewpublisher.ErrUnauthorized)
		})
	}
	for name, graph := range map[string][]byte{
		"placeholder":        []byte(`{"name":"trusted","outputs":{"review_verdict":"approve"}}`),
		"changed expression": reviewGraphJSON(t, strings.ReplaceAll(reviewOutputYAML, "nodes.review.response_text", "inputs.verdict")),
		"duplicate JSON":     []byte(`{"name":"trusted","name":"trusted"}`),
		"unknown JSON":       []byte(`{"name":"trusted","unknown":"ignored"}`),
		"oversized JSON":     []byte(strings.Repeat(" ", 1<<20+1)),
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, validateReviewOutputExpressions([]byte(reviewOutputYAML), graph, true), reviewpublisher.ErrUnauthorized)
		})
	}
	require.ErrorIs(t, validateReviewOutputExpressions([]byte(reviewOutputYAML), graph, false), reviewpublisher.ErrUnauthorized)
}

// Same immutable policy/snapshot and output expression, different model results.
// No policy revision or graph expression is fabricated per verdict.
func TestHistoryAcceptsPinnedOutputExpressionForDifferentVerdicts(t *testing.T) {
	r, a, f := evidenceFixture(t)
	graph := reviewGraphJSON(t, reviewOutputYAML)
	var snapshot GraphSnapshot
	require.NoError(t, json.Unmarshal(a.GraphSnapshot, &snapshot))
	snapshot.Graphs["trusted"] = graph
	snapshot.YAML["trusted"] = []byte(reviewOutputYAML)
	a.GraphSnapshot, _ = json.Marshal(snapshot)
	a.Binding.WorkflowDigest, _ = store.DigestReviewSnapshot(a.GraphSnapshot)
	originalDigest := a.Binding.WorkflowDigest
	f.events[2].GetActivityTaskCompletedEventAttributes().Result = payload(t, map[string]any{"workflow_json": graph, "yaml": []byte(reviewOutputYAML), "has_templates": true})
	for _, verdict := range []reviewpublisher.Verdict{reviewpublisher.Changes, reviewpublisher.Approve} {
		var input CompletionInput
		require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(f.events[3].GetActivityTaskScheduledEventAttributes().Input, &input))
		input.Result.Verdict = verdict
		f.events[3].GetActivityTaskScheduledEventAttributes().Input = payload(t, input)
		f.events[7].GetWorkflowExecutionCompletedEventAttributes().Result = payload(t, map[string]any{"outputs": map[string]any{"review_verdict": verdict}})
		evidence, err := r.Verify(context.Background(), a, "review-run", "private-review-completion", 4, true)
		require.NoError(t, err)
		require.True(t, evidence.Terminal)
		require.True(t, evidence.GraphSucceeded)
		require.Equal(t, verdict, evidence.Verdict)
		require.Equal(t, originalDigest, a.Binding.WorkflowDigest)
	}
}

func TestHistoryOutputExpressionDoesNotRelaxPins(t *testing.T) {
	for _, kind := range []string{"yaml", "graph", "digest", "dependency", "dependency template", "placeholder effective gap", "failed sibling", "mismatched output", "missing status", "invalid verdict"} {
		t.Run(kind, func(t *testing.T) {
			r, a, f := evidenceFixture(t)
			graph := reviewGraphJSON(t, reviewOutputYAML)
			var snapshot GraphSnapshot
			require.NoError(t, json.Unmarshal(a.GraphSnapshot, &snapshot))
			snapshot.Graphs["trusted"] = graph
			snapshot.YAML["trusted"] = []byte(reviewOutputYAML)
			output := map[string]any{"workflow_json": graph, "yaml": []byte(reviewOutputYAML), "has_templates": true}
			switch kind {
			case "yaml":
				output["yaml"] = []byte(strings.ReplaceAll(reviewOutputYAML, "nodes.review.response_text", "inputs.verdict"))
			case "graph":
				output["workflow_json"] = reviewGraphJSON(t, strings.ReplaceAll(reviewOutputYAML, "nodes.review.response_text", "inputs.verdict"))
			case "dependency":
				snapshot.Graphs["unloaded"] = graph
				snapshot.YAML["unloaded"] = []byte(reviewOutputYAML)
			case "dependency template":
				raw := strings.Replace(reviewOutputYAML, "name: trusted", "name: '{{ inputs.name }}'", 1)
				snapshot.Graphs["dependency"] = graph
				snapshot.YAML["dependency"] = []byte(raw)
				f.events[1].GetActivityTaskScheduledEventAttributes().Input = payload(t, map[string]string{"workflow_name": "dependency"})
				output["yaml"] = []byte(raw)
			case "placeholder effective gap":
				// Even when the policy and history agree on this pair, raw YAML
				// must not describe a different graph from the validated JSON.
				raw := strings.Replace(reviewOutputYAML, "name: trusted", "name: '{{ inputs.name }}'", 1)
				snapshot.YAML["trusted"] = []byte(raw)
				output["yaml"] = []byte(raw)
			}
			a.GraphSnapshot, _ = json.Marshal(snapshot)
			a.Binding.WorkflowDigest, _ = store.DigestReviewSnapshot(a.GraphSnapshot)
			if kind == "digest" {
				a.Binding.WorkflowDigest = "sha256:changed"
			}
			f.events[2].GetActivityTaskCompletedEventAttributes().Result = payload(t, output)
			switch kind {
			case "failed sibling":
				f.events[5].GetActivityTaskScheduledEventAttributes().Input = payload(t, map[string]string{"workflow_id": "review-root", "status": "completed", "outcome": "failure"})
			case "mismatched output":
				f.events[7].GetWorkflowExecutionCompletedEventAttributes().Result = payload(t, map[string]any{"outputs": map[string]any{"review_verdict": "changes"}})
			case "missing status":
				f.events = append(f.events[:6], f.events[7:]...)
			case "invalid verdict":
				var input CompletionInput
				require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(f.events[3].GetActivityTaskScheduledEventAttributes().Input, &input))
				input.Result.Verdict = "unknown"
				f.events[3].GetActivityTaskScheduledEventAttributes().Input = payload(t, input)
			}
			evidence, err := r.Verify(context.Background(), a, "review-run", "private-review-completion", 4, true)
			if kind == "failed sibling" || kind == "mismatched output" || kind == "missing status" {
				require.NoError(t, err)
				require.False(t, evidence.GraphSucceeded)
			} else {
				require.Error(t, err)
			}
		})
	}
}
