package handlers

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// routerMockDriver simulates an LLM responding with a tool call containing JSON input.
type routerMockDriver struct {
	responseJSON string // JSON returned as the tool call input
	toolName     string // Name of the response tool to call
	textContent  string // Optional text content alongside the tool call
}

func (m *routerMockDriver) Name() string                          { return "mock" }
func (m *routerMockDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (m *routerMockDriver) ValidateKey(ctx context.Context) error { return nil }

func (m *routerMockDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: "Mock"}, nil
}

func (m *routerMockDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	toolName := m.toolName
	if toolName == "" {
		for _, t := range availableTools {
			toolName = t.Name()
			break
		}
	}

	resp := &llm.DriverResponse{
		Content:      m.textContent,
		FinishReason: "end_turn",
		Usage:        llm.TokenUsage{TokenCount: 50},
	}

	if m.responseJSON != "" {
		resp.ToolCalls = []message.ToolCall{
			{
				ID:    "tc-routing-1",
				Name:  toolName,
				Input: m.responseJSON,
			},
		}
	}

	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: resp,
	}
	close(ch)
	return ch
}

// textOnlyMockDriver simulates an LLM that returns only text, no tool calls.
type textOnlyMockDriver struct {
	textContent string
}

func (m *textOnlyMockDriver) Name() string                          { return "mock" }
func (m *textOnlyMockDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (m *textOnlyMockDriver) ValidateKey(ctx context.Context) error { return nil }

func (m *textOnlyMockDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: m.textContent}, nil
}

func (m *textOnlyMockDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Content:      m.textContent,
			FinishReason: "end_turn",
			Usage:        llm.TokenUsage{TokenCount: 30},
		},
	}
	close(ch)
	return ch
}

// buildResponseToolInput constructs the ActivityInput with a response_tool configured.
func buildResponseToolInput(chatID string, responseTool *reliantv1.ResponseTool) ActivityInput {
	args := &reliantv1.CallLLMArgs{
		SystemPrompt: &reliantv1.CelString{
			Value: &reliantv1.CelString_Literal{Literal: "You are a router."},
		},
		Model: &reliantv1.CelModelSelector{
			Value: &reliantv1.CelModelSelector_Literal{
				Literal: &reliantv1.ModelSelector{Id: "mock-model"},
			},
		},
	}
	if responseTool != nil {
		args.ResponseTool = responseTool
	}
	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID:     chatID,
			Thread:     chatID,
			WorkflowID: "test-wf",
			StepID:     "route__routing_decision",
		},
		Node: &reliantv1.Node{
			Id:   "route__routing_decision",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: args},
		},
	}
}

// newResponseToolSchema builds a structpb.Struct schema for the routing_decision tool.
func newResponseToolSchema(t *testing.T) *structpb.Struct {
	t.Helper()
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"workflow":  map[string]interface{}{"type": "string"},
			"preset":    map[string]interface{}{"type": "string"},
			"prompt":    map[string]interface{}{"type": "string"},
			"reasoning": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"workflow", "preset"},
	}
	s, err := structpb.NewStruct(schema)
	require.NoError(t, err)
	return s
}

func TestCallLLM_ResponseTool_PopulatesResponseData(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-rt-data", "user-rt-data")
	chat := h.CreateTestChat(ctx, "chat-rt-data", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	routingJSON := `{"workflow":"builtin://agent","preset":"general"}`

	mockDriver := &routerMockDriver{
		responseJSON: routingJSON,
		toolName:     "routing_decision",
	}
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activity := NewCallLLMActivity(
		h.Repo(),
		nil,
		nil,
		&staticConfigProvider{},
		driverResolver,
		nil,
	)

	input := buildResponseToolInput(chat.ID, &reliantv1.ResponseTool{
		Name:        &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "routing_decision"}},
		Description: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Select the workflow"}},
		Schema:      newResponseToolSchema(t),
	})

	var output reliantv1.CallLLMOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	// ResponseText should contain the raw JSON from the tool call input
	require.NotEmpty(t, output.ResponseText, "ResponseText must be populated from response_tool's tool call input")
	var parsed map[string]interface{}
	err = json.Unmarshal([]byte(output.ResponseText), &parsed)
	require.NoError(t, err, "ResponseText should be valid JSON")
	assert.Equal(t, "builtin://agent", parsed["workflow"])
	assert.Equal(t, "general", parsed["preset"])

	// ResponseData should be a structpb.Struct with the same values
	require.NotNil(t, output.ResponseData, "ResponseData must be populated when response_tool is configured")
	assert.Equal(t, "builtin://agent", output.ResponseData.Fields["workflow"].GetStringValue())
	assert.Equal(t, "general", output.ResponseData.Fields["preset"].GetStringValue())

	// ToolCalls should still contain the original tool call
	require.Len(t, output.ToolCalls, 1, "ToolCalls should still contain the response tool call")
	assert.Equal(t, "routing_decision", output.ToolCalls[0].Name)
	// Proto-encoded Input may be wrapped in a metadata envelope; verify the raw JSON is recoverable.
	decodedInput, _ := decodeToolCallInputFromProto(output.ToolCalls[0].Input)
	assert.Equal(t, routingJSON, decodedInput)
}

func TestCallLLM_ResponseTool_NoResponseToolConfigured(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-rt-none", "user-rt-none")
	chat := h.CreateTestChat(ctx, "chat-rt-none", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	mockDriver := &textOnlyMockDriver{textContent: "Here is a plain text response"}
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activity := NewCallLLMActivity(
		h.Repo(),
		nil,
		nil,
		&staticConfigProvider{},
		driverResolver,
		nil,
	)

	// No ResponseTool in the input
	input := buildResponseToolInput(chat.ID, nil)

	var output reliantv1.CallLLMOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	// ResponseText should have the text content
	assert.Equal(t, "Here is a plain text response", output.ResponseText)

	// ResponseData should be nil since no response_tool was configured
	assert.Nil(t, output.ResponseData, "ResponseData must be nil when no response_tool is configured")

	// No tool calls
	assert.Empty(t, output.ToolCalls)
}

func TestCallLLM_ResponseTool_TextAndToolCall(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-rt-both", "user-rt-both")
	chat := h.CreateTestChat(ctx, "chat-rt-both", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	routingJSON := `{"workflow":"builtin://agent","preset":"general"}`

	// Driver returns both text and a tool call
	mockDriver := &routerMockDriver{
		responseJSON: routingJSON,
		toolName:     "routing_decision",
		textContent:  "I'll route this to the agent workflow",
	}
	driverResolver := func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return mockDriver, nil
	}

	activity := NewCallLLMActivity(
		h.Repo(),
		nil,
		nil,
		&staticConfigProvider{},
		driverResolver,
		nil,
	)

	input := buildResponseToolInput(chat.ID, &reliantv1.ResponseTool{
		Name:        &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "routing_decision"}},
		Description: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Select the workflow"}},
		Schema:      newResponseToolSchema(t),
	})

	var output reliantv1.CallLLMOutput
	err := h.ExecuteActivity(activity.Execute, input, &output)
	require.NoError(t, err)

	// When a response_tool is configured AND the LLM returns a matching tool call,
	// ResponseText should be the tool call input (overrides any text content)
	require.NotEmpty(t, output.ResponseText, "ResponseText must be populated from tool call input")
	var parsed map[string]interface{}
	err = json.Unmarshal([]byte(output.ResponseText), &parsed)
	require.NoError(t, err, "ResponseText should be valid JSON from tool call, not text content")
	assert.Equal(t, "builtin://agent", parsed["workflow"])
	assert.Equal(t, "general", parsed["preset"])

	// ResponseData should be populated from the tool call
	require.NotNil(t, output.ResponseData, "ResponseData must be populated from response tool call")
	assert.Equal(t, "builtin://agent", output.ResponseData.Fields["workflow"].GetStringValue())
	assert.Equal(t, "general", output.ResponseData.Fields["preset"].GetStringValue())

	// ToolCalls should still contain the original tool call
	require.Len(t, output.ToolCalls, 1)
	assert.Equal(t, "routing_decision", output.ToolCalls[0].Name)
}
