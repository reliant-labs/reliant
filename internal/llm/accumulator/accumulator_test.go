// Copyright (c) 2025 Reliant Labs
package accumulator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// scriptedDriver replays a fixed event sequence, standing in for a provider.
type scriptedDriver struct {
	events []llm.DriverEvent
}

func (d *scriptedDriver) Name() string { return "scripted" }

func (d *scriptedDriver) Model() models.Model {
	return models.Model{ContextWindow: 200000}
}

func (d *scriptedDriver) ValidateKey(ctx context.Context) error { return nil }

func (d *scriptedDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}

func (d *scriptedDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, len(d.events))
	for _, e := range d.events {
		ch <- e
	}
	close(ch)
	return ch
}

// The Anthropic stream shape: content_block_stop carries only the id of the
// block that closed, and the arguments arrive as input_json_delta chunks that
// the driver reassembles into EventComplete. Taking the stop event's ToolCall
// as final therefore yields a call whose Input is the empty string, which
// json.Unmarshal rejects with "unexpected end of JSON input" — the failure
// that made every title generation on claude-4.5-haiku fail on every retry.
func TestStreamAndAccumulate_PrefersFinalResponseToolCalls(t *testing.T) {
	complete := &llm.DriverResponse{
		ToolCalls: []message.ToolCall{{
			ID:       "toolu_1",
			Name:     "set_title",
			Input:    `{"title":"Debugging Workflows"}`,
			Finished: true,
		}},
	}

	d := &scriptedDriver{events: []llm.DriverEvent{
		{Type: llm.EventToolUseStart, ToolCall: &message.ToolCall{ID: "toolu_1", Name: "set_title"}},
		{Type: llm.EventToolUseDelta, ToolCall: &message.ToolCall{ID: "toolu_1", Input: `{"title":`}},
		{Type: llm.EventToolUseDelta, ToolCall: &message.ToolCall{ID: "toolu_1", Input: `"Debugging Workflows"}`}},
		// Anthropic's base driver emits the stop event with an id and nothing else.
		{Type: llm.EventToolUseStop, ToolCall: &message.ToolCall{ID: "toolu_1"}},
		{Type: llm.EventComplete, Response: complete},
	}}

	resp, err := StreamAndAccumulate(context.Background(), d, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)

	assert.Equal(t, `{"title":"Debugging Workflows"}`, resp.ToolCalls[0].Input,
		"arguments must come from the final response, not the argument-less stop event")
	assert.Equal(t, "set_title", resp.ToolCalls[0].Name)
}

// A driver that reports tool calls ONLY through stop events (openrouter, the
// mock and replay drivers) must still be accumulated, so the stop events remain
// the fallback rather than dead code.
func TestStreamAndAccumulate_FallsBackToStopEventToolCalls(t *testing.T) {
	d := &scriptedDriver{events: []llm.DriverEvent{
		{Type: llm.EventToolUseStop, ToolCall: &message.ToolCall{
			ID: "call_1", Name: "set_title", Input: `{"title":"From Stop Event"}`, Finished: true,
		}},
		{Type: llm.EventComplete, Response: &llm.DriverResponse{}},
	}}

	resp, err := StreamAndAccumulate(context.Background(), d, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, `{"title":"From Stop Event"}`, resp.ToolCalls[0].Input)
}

// Text content still accumulates from deltas — compaction reads it.
func TestStreamAndAccumulate_AccumulatesContent(t *testing.T) {
	d := &scriptedDriver{events: []llm.DriverEvent{
		{Type: llm.EventContentDelta, Content: "Hello, "},
		{Type: llm.EventContentDelta, Content: "world"},
		{Type: llm.EventComplete, Response: &llm.DriverResponse{}},
	}}

	resp, err := StreamAndAccumulate(context.Background(), d, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello, world", resp.Content)
}
