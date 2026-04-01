// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// MockLLMDriver is a configurable mock LLM driver for e2e tests.
// It tracks calls and can be configured to return specific responses.
//
// Features:
// - Thread-safe response configuration
// - Call counting for assertions
// - Tool call simulation
// - Response sequencing (different response per call)
type MockLLMDriver struct {
	mu sync.RWMutex

	// Response configuration
	responses  []MockResponse
	callIndex  int
	defaultMsg string

	// Call tracking
	callCount int64
	calls     []MockCall
}

// MockResponse represents a single response from the mock LLM
type MockResponse struct {
	Text      string
	ToolCalls []MockToolCall
}

// MockToolCall represents a tool call to simulate
type MockToolCall struct {
	Name  string
	Input map[string]interface{}
	ID    string // Auto-generated if empty
}

// MockCall records a call to the mock LLM for assertion
type MockCall struct {
	Prompts   []string
	Messages  []message.Message
	Tools     []tools.Tool
	ToolNames []string
}

// NewMockLLMDriver creates a new mock LLM driver
func NewMockLLMDriver() *MockLLMDriver {
	return &MockLLMDriver{
		defaultMsg: "I'm a mock assistant. How can I help you?",
		responses:  []MockResponse{},
	}
}

// SetResponse sets a single text response for all calls
func (d *MockLLMDriver) SetResponse(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.responses = []MockResponse{{Text: text}}
	d.callIndex = 0
}

// SetResponses sets multiple responses that will be returned in sequence
func (d *MockLLMDriver) SetResponses(texts ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.responses = make([]MockResponse, len(texts))
	for i, text := range texts {
		d.responses[i] = MockResponse{Text: text}
	}
	d.callIndex = 0
}

// SetResponseWithToolCall sets a response with a tool call
func (d *MockLLMDriver) SetResponseWithToolCall(text string, toolName string, toolInput map[string]interface{}) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.responses = []MockResponse{{
		Text: text,
		ToolCalls: []MockToolCall{{
			Name:  toolName,
			Input: toolInput,
		}},
	}}
	d.callIndex = 0
}

// SetResponseWithToolCalls sets a response with multiple tool calls
func (d *MockLLMDriver) SetResponseWithToolCalls(text string, toolCalls ...MockToolCall) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.responses = []MockResponse{{
		Text:      text,
		ToolCalls: toolCalls,
	}}
	d.callIndex = 0
}

// AddResponse adds a response to the sequence
func (d *MockLLMDriver) AddResponse(resp MockResponse) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.responses = append(d.responses, resp)
}

// Reset resets the mock state
func (d *MockLLMDriver) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.responses = []MockResponse{}
	d.callIndex = 0
	d.calls = []MockCall{}
	atomic.StoreInt64(&d.callCount, 0)
}

// CallCount returns the number of times the mock was called
func (d *MockLLMDriver) CallCount() int {
	return int(atomic.LoadInt64(&d.callCount))
}

// GetCalls returns all recorded calls
func (d *MockLLMDriver) GetCalls() []MockCall {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]MockCall, len(d.calls))
	for i, call := range d.calls {
		result[i] = MockCall{
			Prompts:   append([]string(nil), call.Prompts...),
			Messages:  cloneMessageSnapshot(call.Messages),
			Tools:     append([]tools.Tool(nil), call.Tools...),
			ToolNames: append([]string(nil), call.ToolNames...),
		}
	}
	return result
}

// getNextResponse returns the next response and advances the index
func (d *MockLLMDriver) getNextResponse() MockResponse {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.responses) == 0 {
		return MockResponse{Text: d.defaultMsg}
	}

	if d.callIndex >= len(d.responses) {
		// After all programmed responses are consumed, return a default
		// no-tool response to avoid infinite loops in agent loops.
		d.callIndex++
		return MockResponse{Text: d.defaultMsg}
	}

	resp := d.responses[d.callIndex]
	d.callIndex++
	return resp
}

// recordCall records a call for later assertion
func (d *MockLLMDriver) recordCall(prompts []string, messages []message.Message, availableTools []tools.Tool) {
	atomic.AddInt64(&d.callCount, 1)

	toolNames := make([]string, 0, len(availableTools))
	for _, availableTool := range availableTools {
		if availableTool == nil {
			continue
		}
		toolNames = append(toolNames, availableTool.Name())
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls = append(d.calls, MockCall{
		Prompts:   append([]string(nil), prompts...),
		Messages:  cloneMessageSnapshot(messages),
		Tools:     append([]tools.Tool(nil), availableTools...),
		ToolNames: toolNames,
	})
}

func cloneMessageSnapshot(messages []message.Message) []message.Message {
	result := make([]message.Message, len(messages))
	for i, msg := range messages {
		copiedMessage := msg
		copiedMessage.Parts = append([]message.ContentPart(nil), msg.Parts...)
		copiedMessage.StateData = append([]byte(nil), msg.StateData...)
		copiedMessage.AgentMetadata = append([]byte(nil), msg.AgentMetadata...)
		result[i] = copiedMessage
	}
	return result
}

// ============================================================================
// llm.Driver interface implementation
// ============================================================================

func (d *MockLLMDriver) Name() string {
	return "mock-e2e-driver"
}

func (d *MockLLMDriver) Model() models.Model {
	return models.Model{
		ID:               "mock",
		Name:             "Mock Model",
		APIModel:         "mock-1.0",
		ContextWindow:    100000,
		DefaultMaxTokens: 4096,
	}
}

func (d *MockLLMDriver) ValidateKey(ctx context.Context) error {
	return nil // Mock always validates
}

func (d *MockLLMDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	d.recordCall(prompts, messages, availableTools)

	resp := d.getNextResponse()

	// Convert tool calls
	var toolCalls []message.ToolCall
	for _, tc := range resp.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("toolu_%s", uuid.New().String()[:8])
		}

		inputJSON := "{}"
		if tc.Input != nil {
			if data, err := json.Marshal(tc.Input); err == nil {
				inputJSON = string(data)
			}
		}

		toolCalls = append(toolCalls, message.ToolCall{
			ID:       id,
			Name:     tc.Name,
			Input:    inputJSON,
			Type:     "tool_use",
			Finished: true,
		})
	}

	finishReason := message.FinishReasonEndTurn
	if len(toolCalls) > 0 {
		finishReason = message.FinishReasonToolUse
	}

	return &llm.DriverResponse{
		Content:      resp.Text,
		ToolCalls:    toolCalls,
		Usage:        llm.TokenUsage{TokenCount: 150},
		FinishReason: finishReason,
	}, nil
}

func (d *MockLLMDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	d.recordCall(prompts, messages, availableTools)

	ch := make(chan llm.DriverEvent, 10)

	go func() {
		defer close(ch)

		resp := d.getNextResponse()
		model := d.Model()

		// Emit content if present
		if resp.Text != "" {
			ch <- llm.DriverEvent{
				Type:  llm.EventContentStart,
				Model: model,
			}

			ch <- llm.DriverEvent{
				Type:    llm.EventContentDelta,
				Model:   model,
				Content: resp.Text,
			}

			ch <- llm.DriverEvent{
				Type:  llm.EventContentStop,
				Model: model,
			}
		}

		// Convert and emit tool calls
		var toolCalls []message.ToolCall
		for _, tc := range resp.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%s", uuid.New().String()[:8])
			}

			inputJSON := "{}"
			if tc.Input != nil {
				if data, err := json.Marshal(tc.Input); err == nil {
					inputJSON = string(data)
				}
			}

			// Emit tool use events
			ch <- llm.DriverEvent{
				Type:  llm.EventToolUseStart,
				Model: model,
				ToolCall: &message.ToolCall{
					ID:       id,
					Name:     tc.Name,
					Finished: false,
				},
			}

			ch <- llm.DriverEvent{
				Type:  llm.EventToolUseDelta,
				Model: model,
				ToolCall: &message.ToolCall{
					ID:       id,
					Input:    inputJSON,
					Finished: false,
				},
			}

			ch <- llm.DriverEvent{
				Type:  llm.EventToolUseStop,
				Model: model,
				ToolCall: &message.ToolCall{
					ID: id,
				},
			}

			toolCalls = append(toolCalls, message.ToolCall{
				ID:       id,
				Name:     tc.Name,
				Input:    inputJSON,
				Type:     "tool_use",
				Finished: true,
			})
		}

		// Determine finish reason
		finishReason := message.FinishReasonEndTurn
		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		// Emit complete event
		ch <- llm.DriverEvent{
			Type:  llm.EventComplete,
			Model: model,
			Response: &llm.DriverResponse{
				Content:      resp.Text,
				ToolCalls:    toolCalls,
				Usage:        llm.TokenUsage{TokenCount: 150},
				FinishReason: finishReason,
			},
		}
	}()

	return ch
}
