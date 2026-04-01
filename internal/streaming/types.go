package streaming

import "encoding/json"

// ============================================================================
// STREAMING DELTA TYPES
// ============================================================================
// These types represent ephemeral streaming events that are broadcast in real-time
// but never persisted to the database. They replace the old "streaming_delta"
// update_type in chat_updates.
// ============================================================================

// DeltaType is the discriminator for streaming delta types
type DeltaType string

const (
	DeltaTypeContentBlockStart DeltaType = "content_block_start"
	DeltaTypeContentBlockDelta DeltaType = "content_block_delta"
	DeltaTypeContentBlockStop  DeltaType = "content_block_stop"
	DeltaTypeMessageStart      DeltaType = "message_start"
	DeltaTypeMessageDelta      DeltaType = "message_delta"
	DeltaTypeMessageStop       DeltaType = "message_stop"
	DeltaTypeToolUse           DeltaType = "tool_use"
	DeltaTypeToolCancelled     DeltaType = "tool_cancelled"
	DeltaTypeTokenCount        DeltaType = "token_count"
)

// StreamingToolCall represents a tool call in streaming context
type StreamingToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input,omitempty"`
}

// StreamingDelta represents an ephemeral streaming event
type StreamingDelta struct {
	UpdateType string    `json:"update_type"` // Always "streaming_delta" for wire format compatibility
	DeltaType  DeltaType `json:"delta_type"`
	Thread     string    `json:"thread,omitempty"`

	// Content block fields
	BlockIndex int    `json:"block_index,omitempty"`
	BlockType  string `json:"block_type,omitempty"`
	Delta      string `json:"delta,omitempty"`

	// Tool call fields
	ToolCall *StreamingToolCall `json:"tool_call,omitempty"`

	// Token usage (total tokens for this message)
	TokenCount int `json:"token_count,omitempty"`

	// Thinking/reasoning fields
	ThinkingSignature string `json:"thinking_signature,omitempty"`

	// Message metadata
	MessageID string `json:"message_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Model     string `json:"model,omitempty"`
}

// MarshalJSON ensures update_type is always "streaming_delta" for wire format
func (d StreamingDelta) MarshalJSON() ([]byte, error) {
	type Alias StreamingDelta
	return json.Marshal(&struct {
		UpdateType string `json:"update_type"`
		Alias
	}{
		UpdateType: "streaming_delta",
		Alias:      Alias(d),
	})
}

// ChatEvent wraps a streaming delta with chat routing info
type ChatEvent struct {
	ChatID string
	Delta  StreamingDelta
}
