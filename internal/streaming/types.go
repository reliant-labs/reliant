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
	DeltaTypeStreamCancelled   DeltaType = "stream_cancelled"
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
	// StreamSeq is a per-message monotonically increasing sequence number
	// stamped by the producer. Consumers use it to detect duplicate or
	// re-streamed deltas after an activity retry (delta identity protocol).
	StreamSeq int64  `json:"stream_seq,omitempty"`
	Role      string `json:"role,omitempty"`
	Model     string `json:"model,omitempty"`
}

// coalescible reports whether this delta is a plain content-text delta that
// can be merged with an adjacent one of the same thread+block. Only additive
// text (content_block_delta carrying Delta text) qualifies — structural deltas
// (block/tool start-stop, cancellations, token counts, thinking signatures)
// must never be merged or dropped, since losing one corrupts the rendered
// message (orphaned tool calls, stuck placeholders).
func (d StreamingDelta) coalescible() bool {
	return d.DeltaType == DeltaTypeContentBlockDelta &&
		d.ToolCall == nil &&
		d.ThinkingSignature == ""
}

// canCoalesceWith reports whether next can be appended onto d without loss:
// both must be coalescible text deltas targeting the same thread, block, and
// message. Merging across message boundaries would splice a retry re-stream's
// text onto the previous attempt's tail, so MessageID must match exactly
// (including both being empty for legacy id-less streams).
func (d StreamingDelta) canCoalesceWith(next StreamingDelta) bool {
	return d.coalescible() && next.coalescible() &&
		d.Thread == next.Thread &&
		d.BlockIndex == next.BlockIndex &&
		d.MessageID == next.MessageID
}

// coalesce returns a delta whose text content is d's followed by next's. The
// caller must have verified canCoalesceWith. Token counts, if present on the
// newer delta, are carried forward so downstream accounting isn't lost.
func (d StreamingDelta) coalesce(next StreamingDelta) StreamingDelta {
	merged := d
	merged.Delta += next.Delta
	if next.TokenCount != 0 {
		merged.TokenCount = next.TokenCount
	}
	// Carry the later stream sequence forward so the merged delta represents
	// the high-water mark of what it contains.
	if next.StreamSeq != 0 {
		merged.StreamSeq = next.StreamSeq
	}
	return merged
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
