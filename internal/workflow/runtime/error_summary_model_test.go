package runtime

import (
	"strings"
	"testing"
)

// The verbatim production failure. `claude-opus-5` is in the catalog and the
// model picker, but the deployment is not entitled to it on Vertex, so every
// turn selecting it 404s. `Available Model Group Fallbacks=None` means the 404
// is terminal — the request fails rather than degrading.
const prodModelNotFoundErr = "failed to stream LLM response: LLM streaming error: " +
	"litellm.NotFoundError: VertexAIException - Publisher model " +
	"`projects/reliant-labs-475814/locations/global/publishers/anthropic/models/claude-opus-5` " +
	"was not found or your project does not have access to it. " +
	"Received Model Group=claude-opus-5\nAvailable Model Group Fallbacks=None"

func TestModelUnavailableIsLegible(t *testing.T) {
	summary := extractLLMErrorSummary(prodModelNotFoundErr)

	if summary == "" {
		t.Fatal("the raw litellm blob still reaches the user unsummarised")
	}

	// Names the model the user chose, so they know which selection to change.
	if !strings.Contains(summary, "claude-opus-5") {
		t.Errorf("summary must name the model, got: %q", summary)
	}

	// Says what to do. The user cannot fix an upstream entitlement, and
	// retrying the same model cannot succeed.
	if !strings.Contains(summary, "different model") {
		t.Errorf("summary must tell the user to pick another model, got: %q", summary)
	}

	// None of the internal vocabulary survives.
	for _, leaked := range []string{
		"litellm",
		"VertexAIException",
		"projects/reliant-labs-475814",
		"Model Group Fallbacks",
		"publishers/anthropic",
	} {
		if strings.Contains(summary, leaked) {
			t.Errorf("summary leaked internal detail %q: %q", leaked, summary)
		}
	}
}

// The "400 Invalid model name" shape from the earlier incident, when the
// proxy's model_list had drifted behind the catalog. Same user-visible
// problem, different upstream wording.
func TestInvalidModelNameIsLegible(t *testing.T) {
	err := "litellm.BadRequestError: Invalid model name passed in model=claude-opus-5. " +
		"Received Model Group=claude-opus-5\nAvailable Model Group Fallbacks=None"

	summary := extractLLMErrorSummary(err)
	if !strings.Contains(summary, "claude-opus-5") || !strings.Contains(summary, "different model") {
		t.Errorf("invalid-model-name summary = %q", summary)
	}
}

// Without the model group marker we cannot name the model, but the advice is
// still correct and still better than the raw blob.
func TestModelUnavailableWithoutModelGroup(t *testing.T) {
	err := "litellm.NotFoundError: VertexAIException - Publisher model was not found. " +
		"Available Model Group Fallbacks=None"

	summary := extractLLMErrorSummary(err)
	if !strings.Contains(summary, "different model") {
		t.Errorf("summary = %q, want generic model-unavailable advice", summary)
	}
}

// The classifier must not claim unrelated failures. Each of these has a
// correct existing summary that the new branch would steal if it matched on a
// bare "not found".
func TestModelUnavailableDoesNotClaimOtherFailures(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want string
	}{
		{
			name: "auth failure naming a model",
			err:  "litellm error: 401 Unauthorized calling model=claude-opus-5",
			want: "Authentication failed with the AI provider",
		},
		{
			name: "network failure",
			err:  "dial tcp 10.0.0.1:4000: connection refused",
			want: networkFailureSummary,
		},
		{
			name: "storage conflict",
			err:  "could not serialize access due to concurrent update (SQLSTATE 40001)",
			want: "Busy saving several things at once — retrying automatically",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractLLMErrorSummary(tc.err); got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// A not-found that is not a gateway routing miss must not be relabelled as a
// model-availability problem.
func TestUnrelatedNotFoundIsNotModelUnavailable(t *testing.T) {
	summary := extractLLMErrorSummary("open /workspace/missing.txt: file was not found")
	if strings.Contains(summary, "different model") {
		t.Errorf("a file-not-found was misread as a model problem: %q", summary)
	}
}
