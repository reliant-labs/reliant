// Copyright (c) 2025 Reliant Labs
package message

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

type MessageRole string

const (
	Assistant MessageRole = "assistant"
	User      MessageRole = "user"
	System    MessageRole = "system"
	Tool      MessageRole = "tool"
	Agent     MessageRole = "agent" // Agent-generated system messages, converted to "user" for LLM APIs
)

type FinishReason string

const (
	FinishReasonEndTurn          FinishReason = "end_turn"
	FinishReasonMaxTokens        FinishReason = "max_tokens"
	FinishReasonToolUse          FinishReason = "tool_use"
	FinishReasonToolUseError     FinishReason = "tool_use_error"
	FinishReasonCancelled        FinishReason = "cancelled"
	FinishReasonError            FinishReason = "error"
	FinishReasonPermissionDenied FinishReason = "permission_denied"

	// FinishReasonRefusal is the provider declining to produce the turn:
	// Anthropic's stop_reason "refusal", OpenAI-shaped "content_filter". It is
	// a COMPLETE turn, not an error — the provider answered, and the answer was
	// no. It is also the one finish reason that routinely arrives with zero
	// content blocks, which is why call_llm keys its content-free-turn
	// substitution on it.
	FinishReasonRefusal FinishReason = "refusal"

	// FinishReasonPauseTurn is the provider pausing mid-turn: Anthropic's
	// stop_reason "pause_turn", emitted when a server-side sampling loop hits
	// its iteration limit and the model expects the same conversation handed
	// back so it can carry on.
	//
	// It is NOT an end of turn. The model did not finish and did not choose to
	// stop; it was suspended. Calling it EndTurn would tell the runtime the
	// answer is complete when it is a fragment.
	//
	// Recognition only, today: nothing acts on this beyond saying so when the
	// paused turn came back empty. Continuing a paused turn means re-sending
	// the assistant turn verbatim — trailing server-tool blocks included, which
	// this package does not model — so it is a deliberate gap, not an
	// oversight. See contentFreeTurnText in the call_llm handler.
	FinishReasonPauseTurn FinishReason = "pause_turn"

	// Should never happen
	FinishReasonUnknown FinishReason = "unknown"
)

type ContentPart interface {
	isPart()
}

type ReasoningContent struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"` // Signature for multi-turn thinking preservation
}

func (tc ReasoningContent) String() string {
	return tc.Thinking
}
func (ReasoningContent) isPart() {}

type TextContent struct {
	Text string `json:"text"`
}

func (tc TextContent) String() string {
	return tc.Text
}

func (TextContent) isPart() {}

type ImageURLContent struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (iuc ImageURLContent) String() string {
	return iuc.URL
}

func (ImageURLContent) isPart() {}

type BinaryContent struct {
	Path     string
	MIMEType string
	Data     []byte
}

func (bc BinaryContent) String(provider models.Family) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if provider == "openai" || provider == "openrouter" {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

func (BinaryContent) isPart() {}

// ToolCall wraps message.ToolCall to implement ContentPart
// ToolCall is the canonical domain model for LLM tool invocation requests.
// This type implements ContentPart for use in message.Message.Parts.
// Related types:
//   - message.ToolCall: alias for this type
//   - tools.ToolCall: alias for this type
//   - mock_static.ToolCall: test fixture with json.RawMessage Input, converts to this
type ToolCall struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Input            string `json:"input"`
	Type             string `json:"type,omitempty"`              // Content block type (streaming)
	Finished         bool   `json:"finished,omitempty"`          // Whether streaming is complete
	BlockIndex       int    `json:"block_index,omitempty"`       // Position in message blocks (workflow activities)
	ThoughtSignature string `json:"thought_signature,omitempty"` // For Gemini 3.x API requirement

	// AvailablePresets tracks which spawn presets were available when the LLM made this call.
	// Used by ExecuteTools to validate the LLM didn't hallucinate a preset.
	// Empty/nil means no validation (non-LLM sources like auditing_agent).
	AvailablePresets []string `json:"available_presets,omitempty"` // Spawn presets available (for spawn tool only)

	// SpawnWorkflow is the target workflow ref for spawn tool calls (e.g., "builtin://auditing-agent").
	// Set by call_llm from the SpawnFilterConfig; used by workflow.go to route spawn execution.
	// Empty means default to "builtin://agent" for backwards compatibility.
	SpawnWorkflow string `json:"spawn_workflow,omitempty"`
}

func (ToolCall) isPart() {}

// ToolResult is the canonical domain model for tool execution results.
// This type implements ContentPart for use in message.Message.Parts.
// Related types:
//   - toolexec.ToolResult: execution-specific (timing, error codes), converts to this
//   - mcp.ToolResult: MCP wire format ([]ToolContent), converts to this
//   - handlers.ToolResult: alias for this type
type ToolResult struct {
	ToolCallID  string          `json:"tool_call_id"`
	Name        string          `json:"name"`
	Content     string          `json:"content"`
	Metadata    string          `json:"metadata,omitempty"`
	IsError     bool            `json:"is_error"`
	BinaryParts []BinaryContent `json:"binary_parts,omitempty"`
}

func (ToolResult) isPart() {}

type Finish struct {
	Reason FinishReason `json:"reason"`
	Time   time.Time    `json:"time"`
}

func (Finish) isPart() {}

type Message struct {
	ID              string
	Role            MessageRole
	SessionID       string
	Ordinal         int64  // Message order within chat
	Thread          string // Thread path (UUID matching workflow ID)
	ContextSequence int64  // Context window version (incremented on compaction)
	Parts           []ContentPart
	Model           models.ModelID
	NextAgent       string          // State transition directive (if any)
	StateData       json.RawMessage // Context data for state transition
	// New fields for agent lane tracking
	AgentID       string          // Unique ID for this agent instance
	ParentAgentID string          // Parent agent ID for nested agents
	AgentState    string          // Current agent state (planning, research, etc)
	AgentMetadata json.RawMessage // Additional agent-specific metadata
	// Token count for context estimation (total tokens for this message)
	TokenCount int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

// TextContents returns ALL TextContent parts (not just the first)
// Useful when a message has multiple text parts (e.g., user text + file contents)
func (m *Message) TextContents() []TextContent {
	textContents := make([]TextContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			textContents = append(textContents, c)
		}
	}
	return textContents
}

func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

func (m *Message) ToolResults() []ToolResult {
	// IMPORTANT: Preserve order from m.Parts to maintain cache control ordering
	// Previously used a map which randomized order, breaking cache control

	// Track seen IDs for deduplication while preserving order
	seen := make(map[string]bool)
	toolResults := make([]ToolResult, 0)

	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			// Only keep the first occurrence of each tool call ID
			if !seen[c.ToolCallID] {
				seen[c.ToolCallID] = true
				toolResults = append(toolResults, c)
			}
		}
	}

	return toolResults
}

func (m *Message) HasToolResponse() bool {
	for _, part := range m.Parts {
		if _, ok := part.(ToolResult); ok {
			return true
		}
	}
	return false
}

func (m *Message) IsFinished() bool {
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

func (m *Message) FinishedAt() *time.Time {
	if finish := m.FinishPart(); finish != nil {
		return &finish.Time
	}
	return nil
}

func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

func (m *Message) IsThinking() bool {
	if m.ReasoningContent().Thinking != "" && m.Content().Text == "" && !m.IsFinished() {
		return true
	}
	return false
}

func (m *Message) AppendContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			m.Parts[i] = TextContent{Text: c.Text + delta}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, TextContent{Text: delta})
	}
}

func (m *Message) AppendReasoningContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{Thinking: c.Thinking + delta}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, ReasoningContent{Thinking: delta})
	}
}

func (m *Message) FinishToolCall(toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:               c.ID,
					Name:             c.Name,
					Input:            c.Input,
					Type:             c.Type,
					Finished:         true,
					BlockIndex:       c.BlockIndex,
					ThoughtSignature: c.ThoughtSignature,
					AvailablePresets: c.AvailablePresets,
				}
				return
			}
		}
	}
}

func (m *Message) AppendToolCallInput(toolCallID string, inputDelta string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:               c.ID,
					Name:             c.Name,
					Input:            c.Input + inputDelta,
					Type:             c.Type,
					Finished:         c.Finished,
					BlockIndex:       c.BlockIndex,
					ThoughtSignature: c.ThoughtSignature,
					AvailablePresets: c.AvailablePresets,
				}
				return
			}
		}
	}
}

func (m *Message) AddToolCall(tc ToolCall) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == tc.ID {
				m.Parts[i] = tc
				return
			}
		}
	}
	m.Parts = append(m.Parts, tc)
}

func (m *Message) SetToolCalls(tc []ToolCall) {
	// remove any existing tool call part it could have multiple
	parts := make([]ContentPart, 0)
	for _, part := range m.Parts {
		if _, ok := part.(ToolCall); ok {
			continue
		}
		parts = append(parts, part)
	}
	m.Parts = parts
	for _, toolCall := range tc {
		m.Parts = append(m.Parts, toolCall)
	}
}

func (m *Message) AddToolResult(tr ToolResult) {
	m.Parts = append(m.Parts, tr)
}

func (m *Message) AddFinish(reason FinishReason) {
	// remove any existing finish part
	for i, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			m.Parts = slices.Delete(m.Parts, i, i+1)
			break
		}
	}
	m.Parts = append(m.Parts, Finish{Reason: reason, Time: time.Now()})
}

func (m *Message) AddImageURL(url, detail string) {
	m.Parts = append(m.Parts, ImageURLContent{URL: url, Detail: detail})
}

func (m *Message) AddBinary(mimeType string, data []byte) {
	m.Parts = append(m.Parts, BinaryContent{MIMEType: mimeType, Data: data})
}
