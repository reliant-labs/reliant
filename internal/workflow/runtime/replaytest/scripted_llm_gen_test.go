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

// Consumed reports how many scripted turns the agent loop actually played.
//
// Generators assert this equals the script length. Exhausted() alone is not
// enough: it only catches the loop asking for MORE turns than scripted, and
// the damaging failure is the opposite one. When an auxiliary request stole a
// turn, every scenario ran one turn short — the agent loop received a later
// turn than intended, ended early, and the exported history was a truncated
// shape (agent_tool_loop lost its ExecuteTools entirely) that still replayed
// green, pinning the wrong contract. Under-consumption is silent unless
// something checks for it, so this is what checks for it.
func (s *ScriptedLLM) Consumed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Scripted reports how many turns were scripted.
func (s *ScriptedLLM) Scripted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns)
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

// SendMessages is part of the llm.Driver interface but is no longer on any
// production path this harness drives — every auxiliary consumer streams. It
// returns a canned response so an unexpected caller is harmless, and, like the
// auxiliary paths below, never consumes the scripted agent-loop turns.
func (s *ScriptedLLM) SendMessages(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{
		Content:      "scripted title",
		FinishReason: message.FinishReasonEndTurn,
		Usage:        llm.TokenUsage{TokenCount: 10, InputTokens: 5, OutputTokens: 5},
	}, nil
}

// StreamResponse plays the next scripted turn using the same event protocol
// as the real drivers.
//
// AUXILIARY REQUESTS MUST NOT CONSUME THE SCRIPT. Two production consumers
// share this injected driver with the agent loop but are not part of any
// scenario's turn sequence: the compaction summary and chat title generation.
// Both are recognized here and answered with a canned reply.
//
// Titling is the one that bit us. It used to call SendMessages, so routing it
// away from the script was automatic. #229 switched it to
// accumulator.StreamAndAccumulate — the Codex backend requires stream: true and
// answers a non-streaming request with a bare 400, which became reachable once
// title model selection opened up beyond Anthropic. That made titling land
// HERE, where it silently ate turn 1 of every scenario, because
// GenerateTitleWorkflow is dispatched by CreateChat and races the agent loop.
// Every scripted scenario then ran one turn off-by-one. It is recognized by the
// tool the request is pinned to (set_title), which is the request's actual
// identity rather than prompt wording that can be reworded.
func (s *ScriptedLLM) StreamResponse(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	s.mu.Lock()

	if isCompactionRequest(prompts) {
		s.mu.Unlock()
		return s.streamCanned(Turn{Text: compactionSummaryText, TokenCount: 20})
	}

	// Answer with the pinned set_title call the production path reads, so
	// titling exercises its real tool-call extraction instead of falling back
	// to the truncated first message.
	if isTitleRequest(tls) {
		s.mu.Unlock()
		return s.streamCanned(Turn{
			ToolCalls: []message.ToolCall{
				ToolCall("call-set-title-1", tools.SetTitleToolName,
					`{"`+tools.SetTitleTitleField+`":"Scripted Fixture Chat"}`),
			},
			TokenCount: 10,
		})
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

// isTitleRequest reports whether this is the title-generation call. That
// request pins tool_choice to set_title and offers nothing else, so the tool
// list identifies it exactly — and unlike a prompt substring, it cannot drift
// when the prompt is reworded.
func isTitleRequest(tls []tools.Tool) bool {
	if len(tls) != 1 {
		return false
	}
	return tls[0].Name() == tools.SetTitleToolName
}
