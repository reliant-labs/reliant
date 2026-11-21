// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
)

func TestIsThoughtSignatureRejection(t *testing.T) {
	t.Parallel()
	rejected := []string{
		// The wordings hermes-agent's Gemini and Vertex fakes reproduce.
		"Corrupted thought signature.",
		"Function call is missing a thought_signature in functionCall parts. This is required for tools to work correctly, and missing thought_signature may lead to degraded model performance. Additional data, function call `default_api:bash` , position 2. Please refer to https://ai.google.dev/gemini-api/docs/thought-signatures for more details.",
		"Function call `bash` in the `3.` content block is missing a `thought_signature`.",
		"contents[2].parts[0].thought_signature: invalid bytes value",
		// As the genai SDK wraps a 400.
		`Error 400, Message: Corrupted thought signature., Status: INVALID_ARGUMENT, Details: []`,
		// As antigravity wraps one.
		`antigravity: API request failed with status 400: {"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`,
		"Unable to submit request because Thought signature is not valid.",
	}
	for _, msg := range rejected {
		assert.True(t, IsThoughtSignatureRejection(errors.New(msg)), msg)
		assert.True(t, IsThoughtSignatureRejection(fmt.Errorf("gemini API error: %w", errors.New(msg))), msg)
	}

	notRejected := []string{
		"Error 400, Message: Request contains an invalid argument., Status: INVALID_ARGUMENT",
		"Please ensure that the number of function response parts is equal to the number of function call parts of the function call turn.",
		"Error 503, Message: The model is overloaded., Status: UNAVAILABLE",
		"thought signature captured", // the phrase without a verdict
	}
	for _, msg := range notRejected {
		assert.False(t, IsThoughtSignatureRejection(errors.New(msg)), msg)
	}
	assert.False(t, IsThoughtSignatureRejection(nil))
	assert.True(t, IsThoughtSignatureRejection(ErrThoughtSignatureRejected))
}

// The stand-in must reach the wire as the documented literal. The genai SDK
// marshals Part.ThoughtSignature ([]byte) as standard base64; protobuf JSON
// accepts either alphabet, so what matters is that the server decodes the
// same bytes from what the SDK sends as it does from the literal.
func TestSkipThoughtSignatureValidatorBytesOnTheWire(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(&genai.Part{ThoughtSignature: SkipThoughtSignatureValidatorBytes()})
	require.NoError(t, err)
	var wire struct {
		ThoughtSignature string `json:"thoughtSignature"`
	}
	require.NoError(t, json.Unmarshal(body, &wire))

	sent, err := base64.StdEncoding.DecodeString(wire.ThoughtSignature)
	require.NoError(t, err)
	literal, err := base64.RawURLEncoding.DecodeString(SkipThoughtSignatureValidator)
	require.NoError(t, err)
	assert.Equal(t, literal, sent, "the server must decode the stand-in's bytes")
	assert.Equal(t, "skip/thought/signature/validator", wire.ThoughtSignature,
		"the literal in the standard alphabet")
	assert.NotEqual(t, []byte(SkipThoughtSignatureValidator), SkipThoughtSignatureValidatorBytes(),
		"the ASCII of the literal would reach the wire as a different, invalid signature")
}

func TestValidatesThoughtSignatures(t *testing.T) {
	t.Parallel()
	for model, want := range map[string]bool{
		"gemini-3-pro-preview":                    true,
		"gemini-3.5-flash":                        true,
		"gemini-4-hydrogen":                       true,
		"gemini-2.5-pro":                          false,
		"gemini-2.0-flash-lite":                   false,
		"gemini-1.5-pro":                          false,
		"publishers/google/models/gemini-2.5-pro": false,
		"some-future-model":                       true,
	} {
		assert.Equal(t, want, ValidatesThoughtSignatures(model), model)
	}
}

func call(name string, sig []byte) *genai.Part {
	return &genai.Part{FunctionCall: &genai.FunctionCall{Name: name}, ThoughtSignature: sig}
}

func TestStampUnsignedSteps(t *testing.T) {
	t.Parallel()
	signed := []byte("real-signature")
	contents := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "go"}}},
		// Unsigned parallel calls: only the first gets the stand-in.
		{Role: "model", Parts: []*genai.Part{{Text: "ok"}, call("a", nil), call("b", nil)}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "a"}}}},
		// Gemini's own parallel shape: first call signed, sibling not.
		{Role: "model", Parts: []*genai.Part{call("c", signed), call("d", nil)}},
	}
	assert.Equal(t, 1, StampUnsignedSteps(contents))
	assert.Equal(t, SkipThoughtSignatureValidatorBytes(), contents[1].Parts[1].ThoughtSignature)
	assert.Nil(t, contents[1].Parts[2].ThoughtSignature)
	assert.Equal(t, signed, contents[3].Parts[0].ThoughtSignature, "a real signature stays where it arrived")
	assert.Nil(t, contents[3].Parts[1].ThoughtSignature)
}

func TestReplaceSignaturesWithStandIns(t *testing.T) {
	t.Parallel()
	contents := []*genai.Content{
		{Role: "model", Parts: []*genai.Part{{Text: "thinking", ThoughtSignature: []byte("t")}, call("c", []byte("x")), call("d", nil)}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "c"}, ThoughtSignature: []byte("x")}}},
	}
	assert.Equal(t, 3, ReplaceSignaturesWithStandIns(contents))
	assert.Nil(t, contents[0].Parts[0].ThoughtSignature)
	assert.Equal(t, SkipThoughtSignatureValidatorBytes(), contents[0].Parts[1].ThoughtSignature)
	assert.Nil(t, contents[0].Parts[2].ThoughtSignature)
	assert.Nil(t, contents[1].Parts[0].ThoughtSignature)
}

// fakeAttempts serves one canned event list per attempt and records which
// attempts ran.
type fakeAttempts struct {
	events map[bool][]llm.DriverEvent
	ran    []bool
}

func (f *fakeAttempts) attempt(standIns bool) <-chan llm.DriverEvent {
	f.ran = append(f.ran, standIns)
	ch := make(chan llm.DriverEvent)
	go func() {
		defer close(ch)
		for _, ev := range f.events[standIns] {
			ch <- ev
		}
	}()
	return ch
}

func drain(ch <-chan llm.DriverEvent) []llm.DriverEvent {
	var out []llm.DriverEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestRecoverSignatureRejection(t *testing.T) {
	t.Parallel()
	rejection := errors.New("Error 400, Message: Corrupted thought signature., Status: INVALID_ARGUMENT")
	ok := llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "done"}}

	t.Run("a rejection before output is retried once with stand-ins", func(t *testing.T) {
		t.Parallel()
		f := &fakeAttempts{events: map[bool][]llm.DriverEvent{
			false: {{Type: llm.EventError, Error: rejection}, {Type: llm.EventComplete, Response: &llm.DriverResponse{}}},
			true:  {{Type: llm.EventContentDelta, Content: "done"}, ok},
		}}
		got := drain(RecoverSignatureRejection(context.Background(), SourceGemini, f.attempt))
		assert.Equal(t, []bool{false, true}, f.ran)
		require.Len(t, got, 2, "the first attempt's events are dropped")
		assert.Equal(t, llm.EventContentDelta, got[0].Type)
		assert.Equal(t, ok, got[1])
	})

	t.Run("a rejection of the retry is not retried again", func(t *testing.T) {
		t.Parallel()
		f := &fakeAttempts{events: map[bool][]llm.DriverEvent{
			false: {{Type: llm.EventError, Error: rejection}},
			true:  {{Type: llm.EventError, Error: rejection}},
		}}
		got := drain(RecoverSignatureRejection(context.Background(), SourceGemini, f.attempt))
		assert.Equal(t, []bool{false, true}, f.ran)
		require.Len(t, got, 1)
		assert.Equal(t, rejection, got[0].Error)
	})

	t.Run("a rejection after output is passed through", func(t *testing.T) {
		t.Parallel()
		f := &fakeAttempts{events: map[bool][]llm.DriverEvent{
			false: {{Type: llm.EventContentDelta, Content: "x"}, {Type: llm.EventError, Error: rejection}},
		}}
		got := drain(RecoverSignatureRejection(context.Background(), SourceGemini, f.attempt))
		assert.Equal(t, []bool{false}, f.ran)
		require.Len(t, got, 2)
		assert.Equal(t, rejection, got[1].Error)
	})

	t.Run("another error is passed through", func(t *testing.T) {
		t.Parallel()
		other := errors.New("Error 503, Message: overloaded")
		f := &fakeAttempts{events: map[bool][]llm.DriverEvent{
			false: {{Type: llm.EventRetryWait, Retry: &llm.RetryWait{}}, {Type: llm.EventError, Error: other}},
		}}
		got := drain(RecoverSignatureRejection(context.Background(), SourceGemini, f.attempt))
		assert.Equal(t, []bool{false}, f.ran)
		require.Len(t, got, 2)
		assert.Equal(t, other, got[1].Error)
	})

	t.Run("a missing-signature stop is recovered", func(t *testing.T) {
		t.Parallel()
		f := &fakeAttempts{events: map[bool][]llm.DriverEvent{
			false: {{Type: llm.EventError, Error: StopFailure(SourceGemini, "MISSING_THOUGHT_SIGNATURE", "")}},
			true:  {ok},
		}}
		got := drain(RecoverSignatureRejection(context.Background(), SourceGemini, f.attempt))
		assert.Equal(t, []bool{false, true}, f.ran)
		assert.Equal(t, []llm.DriverEvent{ok}, got)
	})
}

func TestRecoverSignatureRejectionSync(t *testing.T) {
	t.Parallel()
	var ran []bool
	got, err := RecoverSignatureRejectionSync(context.Background(), SourceVertexAI, func(standIns bool) (string, error) {
		ran = append(ran, standIns)
		if !standIns {
			return "", errors.New("Corrupted thought signature.")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.Equal(t, []bool{false, true}, ran)

	ran = nil
	_, err = RecoverSignatureRejectionSync(context.Background(), SourceVertexAI, func(standIns bool) (string, error) {
		ran = append(ran, standIns)
		return "", errors.New("Error 500 internal")
	})
	require.Error(t, err)
	assert.Equal(t, []bool{false}, ran)
}
