// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"context"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// This file is a trimmed replica of the scripted-LLM seam used by the e2e
// story suite (e2e/stories/scripted_llm_test.go). That package is test-only
// and build-tagged (e2e), so its pieces are not importable; the minimal
// driver is replicated here for fixture generation. Behavioral contract kept
// identical: the agent loop (CallLLM → StreamResponse) consumes scripted
// turns in order; title generation (SendMessages) and compaction summary
// requests (StreamResponse with the "summarizing conversations" prompt) get
// canned responses and never consume the script.

// compactionPromptMarker identifies the Compact activity's summary request.
const compactionPromptMarker = "summarizing conversations"

// compactionSummaryText is the canned compaction summary.
const compactionSummaryText = "The user asked for token-heavy work; a command was run successfully. Next step: finish up."

// Turn is one scripted assistant reply for the agent loop.
type Turn struct {
	Text       string
	ToolCalls  []message.ToolCall
	TokenCount int64 // reported usage; drives compaction edges. Defaults to 50.
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

// ScriptedLLM is a thread-safe scripted llm.Driver.
type ScriptedLLM struct {
	mu        sync.Mutex
	turns     []Turn
	next      int
	exhausted bool
}

// NewScriptedLLM builds a driver that plays the given turns in order.
func NewScriptedLLM(turns ...Turn) *ScriptedLLM {
	return &ScriptedLLM{turns: turns}
}

// Exhausted reports whether the agent loop asked for more turns than scripted.
func (s *ScriptedLLM) Exhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exhausted
}

func (s *ScriptedLLM) Name() string { return "scripted-replayfixtures" }

func (s *ScriptedLLM) Model() models.Model {
	return models.Model{
		ID:               "mock",
		Name:             "Scripted Replay-Fixture Mock",
		APIModel:         "mock",
		ContextWindow:    200000,
		DefaultMaxTokens: 8192,
	}
}

func (s *ScriptedLLM) ValidateKey(ctx context.Context) error { return nil }

// SendMessages serves auxiliary consumers (title generation). It never
// consumes the scripted agent-loop turns.
func (s *ScriptedLLM) SendMessages(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{
		Content:      "scripted title",
		FinishReason: message.FinishReasonEndTurn,
		Usage:        llm.TokenUsage{TokenCount: 10, InputTokens: 5, OutputTokens: 5},
	}, nil
}

// StreamResponse plays the next scripted turn using the same event protocol
// as the real drivers. Compaction summary requests are routed to a canned
// summary and never consume the script.
func (s *ScriptedLLM) StreamResponse(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	s.mu.Lock()

	if isCompactionRequest(prompts) {
		s.mu.Unlock()
		return s.streamCanned(Turn{Text: compactionSummaryText, TokenCount: 20})
	}

	var turn Turn
	if s.next < len(s.turns) {
		turn = s.turns[s.next]
		s.next++
	} else {
		// Do not wedge the workflow: reply with plain text and no tool calls
		// so the agent loop terminates. Generators assert !Exhausted().
		s.exhausted = true
		turn = Turn{Text: "SCRIPT EXHAUSTED: the fixture script ran out of turns."}
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

func isCompactionRequest(prompts []string) bool {
	for _, p := range prompts {
		if strings.Contains(p, compactionPromptMarker) {
			return true
		}
	}
	return false
}
