// Copyright (c) 2025 Reliant Labs
package mockstatic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Scenario represents a test scenario for agent interactions
type Scenario struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	WorkDir     string     `json:"work_dir,omitempty"`
	Exchanges   []Exchange `json:"exchanges"`
}

// Exchange represents a single request-response exchange
type Exchange struct {
	UserPrompt        string             `json:"user_prompt,omitempty"`
	AssistantResponse *AssistantResponse `json:"assistant_response"`
	Validations       []Validation       `json:"validations,omitempty"`
}

// AssistantResponse represents what the mocked LLM returns
type AssistantResponse struct {
	Message   string     `json:"message,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents a complete tool call with parameters
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Validation checks for expected behavior after tool execution
type Validation struct {
	Type                 string   `json:"type"`
	ToolResponseContains []string `json:"tool_response_contains,omitempty"`
	FileExists           string   `json:"file_exists,omitempty"`
	FileContains         []string `json:"file_contains,omitempty"`
	BashOutputContains   []string `json:"bash_output_contains,omitempty"`
	ErrorExpected        bool     `json:"error_expected,omitempty"`
}

// ScenarioDriver mocks an LLM using predefined scenario exchanges
type ScenarioDriver struct {
	model           models.Model
	exchanges       []Exchange
	currentExchange int
}

// Name returns the name of the driver
func (d *ScenarioDriver) Name() string {
	return "scenario"
}

// NewScenarioDriver creates a new scenario-based mock driver
func NewScenarioDriver(scenario *Scenario) *ScenarioDriver {
	return &ScenarioDriver{
		model: models.Model{
			Name: "mock-scenario",
		},
		exchanges:       scenario.Exchanges,
		currentExchange: 0,
	}
}

func (d *ScenarioDriver) Model() models.Model {
	return d.model
}

func (d *ScenarioDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	if d.currentExchange >= len(d.exchanges) {
		return nil, fmt.Errorf("scenario driver: no more exchanges available (at exchange %d of %d)", d.currentExchange+1, len(d.exchanges))
	}

	exchange := d.exchanges[d.currentExchange]
	d.currentExchange++

	finishReason := message.FinishReasonEndTurn
	if len(exchange.AssistantResponse.ToolCalls) > 0 {
		finishReason = message.FinishReasonToolUse
	}
	response := &llm.DriverResponse{
		Content:      exchange.AssistantResponse.Message,
		FinishReason: finishReason,
	}

	// Convert scenario tool calls to message.ToolCall format
	if len(exchange.AssistantResponse.ToolCalls) > 0 {
		response.ToolCalls = make([]message.ToolCall, len(exchange.AssistantResponse.ToolCalls))
		for i, tc := range exchange.AssistantResponse.ToolCalls {
			// Convert json.RawMessage to string
			inputStr := string(tc.Input)

			response.ToolCalls[i] = message.ToolCall{
				ID:       tc.ID,
				Name:     tc.Name,
				Input:    inputStr,
				Type:     "function",
				Finished: true,
			}
		}
		response.FinishReason = message.FinishReasonToolUse
	}

	return response, nil
}

func (d *ScenarioDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)

	go func() {
		defer close(ch)

		resp, err := d.SendMessages(ctx, prompts, messages, tools)
		if err != nil {
			ch <- llm.DriverEvent{Error: err}
			return
		}
		// Stream content first
		if resp.Content != "" {
			ch <- llm.DriverEvent{
				Type:    llm.EventContentStart,
				Content: resp.Content,
			}
		}

		// Then stream tool calls
		for _, toolCall := range resp.ToolCalls {
			ch <- llm.DriverEvent{
				Type:     llm.EventToolUseStart,
				ToolCall: &toolCall,
			}
			ch <- llm.DriverEvent{
				Type:     llm.EventToolUseStop,
				ToolCall: &toolCall,
			}
		}

		// Send complete event
		ch <- llm.DriverEvent{
			Type:     llm.EventComplete,
			Response: resp,
		}
	}()

	return ch
}

func (d *ScenarioDriver) Reset() {
	d.currentExchange = 0
}

func (d *ScenarioDriver) GetCurrentExchange() int {
	return d.currentExchange
}

func (d *ScenarioDriver) ValidateKey(ctx context.Context) error {
	// Mock driver always validates successfully
	return nil
}

// ParseToolInput parses the tool input JSON into a map
func ParseToolInput(input json.RawMessage) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := json.Unmarshal(input, &result); err != nil {
		return nil, err
	}
	return result, nil
}
