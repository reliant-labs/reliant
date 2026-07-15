// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"context"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// compactionPromptMarker identifies the compaction summary request built by
// the Compact activity ("You are a specialist at summarizing
// conversations."). Compaction shares the agent loop's streaming path, so the
// scripted driver routes it to a canned summary instead of consuming the
// story's turns.
const compactionPromptMarker = "summarizing conversations"

// CompactionSummaryText is what the scripted driver returns for compaction
// summary requests. Stories assert it round-trips into the new context
// window.
const CompactionSummaryText = "The user asked for token-heavy work; a command was run successfully. Next step: finish up."

// Turn is one scripted assistant reply for the agent loop (consumed by
// StreamResponse, which is what the CallLLM activity uses).
type Turn struct {
	// Text is the assistant message text.
	Text string
	// ToolCalls the assistant "requests". Input must be a JSON string.
	ToolCalls []message.ToolCall
	// TokenCount reported on the response usage (drives thread token
	// accounting / the compaction edge). Defaults to 50 when zero.
	TokenCount int64
}

// ToolCall is a convenience constructor for a scripted tool call.
func ToolCall(id, name, inputJSON string) message.ToolCall {
	return message.ToolCall{
		ID:       id,
		Name:     name,
		Input:    inputJSON,
		Type:     "function",
		Finished: true,
	}
}

// LLMCall records one StreamResponse invocation for assertions.
type LLMCall struct {
	Prompts   []string
	Messages  []message.Message
	ToolNames []string
}

// ScriptedLLM is a thread-safe scripted llm.Driver.
//
// The agent loop (CallLLM activity) consumes turns via StreamResponse in
// order. Auxiliary LLM consumers that share the injected resolver — title
// generation and compaction — use SendMessages, which returns a static canned
// response and does NOT consume the script. That separation keeps stories
// deterministic even though GenerateTitleWorkflow runs concurrently with the
// agent workflow.
type ScriptedLLM struct {
	mu sync.Mutex

	turns []Turn
	next  int

	exhausted bool

	streamCalls     []LLMCall
	sendCalls       []LLMCall
	compactionCalls []LLMCall

	// sendText is returned by SendMessages (title generation).
	sendText string
}

// NewScriptedLLM builds a driver that will play the given turns in order.
func NewScriptedLLM(turns ...Turn) *ScriptedLLM {
	return &ScriptedLLM{
		turns:    turns,
		sendText: "scripted summary",
	}
}

// Append adds more turns to the script (e.g. after a pause/resume).
func (s *ScriptedLLM) Append(turns ...Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, turns...)
}

// Exhausted reports whether the agent loop asked for more turns than were
// scripted. Stories assert this is false at the end.
func (s *ScriptedLLM) Exhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exhausted
}

// StreamCalls returns a snapshot of the recorded agent-loop calls.
func (s *ScriptedLLM) StreamCalls() []LLMCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LLMCall, len(s.streamCalls))
	copy(out, s.streamCalls)
	return out
}

// SendCalls returns a snapshot of the recorded SendMessages calls
// (title generation).
func (s *ScriptedLLM) SendCalls() []LLMCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LLMCall, len(s.sendCalls))
	copy(out, s.sendCalls)
	return out
}

// CompactionCalls returns the recorded compaction summary requests.
func (s *ScriptedLLM) CompactionCalls() []LLMCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LLMCall, len(s.compactionCalls))
	copy(out, s.compactionCalls)
	return out
}

func (s *ScriptedLLM) Name() string { return "scripted-e2e" }

func (s *ScriptedLLM) Model() models.Model {
	return models.Model{
		ID:               "mock",
		Name:             "Scripted E2E Mock",
		APIModel:         "mock",
		ContextWindow:    200000,
		DefaultMaxTokens: 8192,
	}
}

func (s *ScriptedLLM) ValidateKey(ctx context.Context) error { return nil }

// SendMessages serves the auxiliary consumers (title generation, compaction
// summaries). It never consumes the scripted agent-loop turns.
func (s *ScriptedLLM) SendMessages(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) (*llm.DriverResponse, error) {
	s.mu.Lock()
	s.sendCalls = append(s.sendCalls, newLLMCall(prompts, msgs, tls))
	text := s.sendText
	s.mu.Unlock()

	return &llm.DriverResponse{
		Content:      text,
		FinishReason: message.FinishReasonEndTurn,
		Usage:        llm.TokenUsage{TokenCount: 10, InputTokens: 5, OutputTokens: 5},
	}, nil
}

// StreamResponse plays the next scripted turn using the same event protocol
// as the real drivers (content, tool_use start/stop, complete). Compaction
// summary requests (which share this path) are routed to a canned summary
// and never consume the story's turns.
func (s *ScriptedLLM) StreamResponse(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	s.mu.Lock()

	if isCompactionRequest(prompts) {
		s.compactionCalls = append(s.compactionCalls, newLLMCall(prompts, msgs, tls))
		s.mu.Unlock()
		return s.streamCanned(Turn{Text: CompactionSummaryText, TokenCount: 20})
	}

	s.streamCalls = append(s.streamCalls, newLLMCall(prompts, msgs, tls))

	var turn Turn
	if s.next < len(s.turns) {
		turn = s.turns[s.next]
		s.next++
	} else {
		// Do not wedge the workflow: reply with plain text and no tool calls
		// so the agent loop terminates. Stories assert !Exhausted().
		s.exhausted = true
		turn = Turn{Text: "SCRIPT EXHAUSTED: the test script ran out of turns."}
	}
	s.mu.Unlock()

	return s.streamCanned(turn)
}

// streamCanned emits one turn using the standard driver event protocol.
func (s *ScriptedLLM) streamCanned(turn Turn) <-chan llm.DriverEvent {
	tokenCount := turn.TokenCount
	if tokenCount == 0 {
		tokenCount = 50
	}

	finish := message.FinishReasonEndTurn
	if len(turn.ToolCalls) > 0 {
		finish = message.FinishReasonToolUse
	}
	resp := &llm.DriverResponse{
		Content:      turn.Text,
		ToolCalls:    turn.ToolCalls,
		FinishReason: finish,
		Usage: llm.TokenUsage{
			TokenCount:   tokenCount,
			InputTokens:  tokenCount / 2,
			OutputTokens: tokenCount - tokenCount/2,
		},
	}

	model := s.Model()
	ch := make(chan llm.DriverEvent, len(turn.ToolCalls)*2+2)
	go func() {
		defer close(ch)
		if resp.Content != "" {
			ch <- llm.DriverEvent{Type: llm.EventContentStart, Model: model, Content: resp.Content}
		}
		for i := range resp.ToolCalls {
			tc := resp.ToolCalls[i]
			ch <- llm.DriverEvent{Type: llm.EventToolUseStart, Model: model, ToolCall: &tc}
			ch <- llm.DriverEvent{Type: llm.EventToolUseStop, Model: model, ToolCall: &tc}
		}
		ch <- llm.DriverEvent{Type: llm.EventComplete, Model: model, Response: resp}
	}()
	return ch
}

// isCompactionRequest detects the Compact activity's summary request by its
// distinctive system prompt.
func isCompactionRequest(prompts []string) bool {
	for _, p := range prompts {
		if strings.Contains(p, compactionPromptMarker) {
			return true
		}
	}
	return false
}

func newLLMCall(prompts []string, msgs []message.Message, tls []tools.Tool) LLMCall {
	names := make([]string, 0, len(tls))
	for _, t := range tls {
		names = append(names, t.Name())
	}
	return LLMCall{Prompts: prompts, Messages: msgs, ToolNames: names}
}
