// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm"
	openaidriver "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type stubTool struct {
	name string
}

func (s stubTool) Name() string { return s.name }

func (s stubTool) Description() string { return "stub" }

func (s stubTool) ParamSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}

func (s stubTool) RequiresPermission(_ *rctx.ToolContext, _ llmtools.ToolCall) (bool, error) {
	return false, nil
}

func (s stubTool) Run(_ *rctx.ToolContext, _ llmtools.ToolCall) (llmtools.ToolResponse, error) {
	return llmtools.NewTextResponse("ok"), nil
}

// testToken is a valid JWT structure for testing (not a real token)
// Contains chatgpt_account_id: "376f7e87-6a8b-4d8b-896e-cca5c1a9b35f"
// #nosec G101 -- test fixture token for JWT parsing tests; not a production credential
const testToken = "eyJhbGciOiJSUzI1NiIsImtpZCI6IjE5MzQ0ZTY1LWJiYzktNDRkMS1hOWQwLWY5NTdiMDc5YmQwZSIsInR5cCI6IkpXVCJ9.eyJhdWQiOlsiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS92MSJdLCJjbGllbnRfaWQiOiJhcHBfRU1vYW1FRVo3M2YwQ2tYYVhwN2hyYW5uIiwiZXhwIjo5OTk5OTk5OTk5LCJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiMzc2ZjdlODctNmE4Yi00ZDhiLTg5NmUtY2NhNWMxYTliMzVmIiwiY2hhdGdwdF9hY2NvdW50X3VzZXJfaWQiOiJ1c2VyLUIwcWpFM2pYUWFVUmlnamFRNEtFQzl3al9fMzc2ZjdlODctNmE4Yi00ZDhiLTg5NmUtY2NhNWMxYTliMzVmIiwiY2hhdGdwdF9jb21wdXRlX3Jlc2lkZW5jeSI6Im5vX2NvbnN0cmFpbnQiLCJjaGF0Z3B0X3BsYW5fdHlwZSI6InBsdXMiLCJjaGF0Z3B0X3VzZXJfaWQiOiJ1c2VyLUIwcWpFM2pYUWFVUmlnamFRNEtFQzl3aiIsInVzZXJfaWQiOiJ1c2VyLUIwcWpFM2pYUWFVUmlnamFRNEtFQzl3aiJ9LCJodHRwczovL2FwaS5vcGVuYWkuY29tL3Byb2ZpbGUiOnsiZW1haWwiOiJ0ZXN0QHRlc3QuY29tIiwiZW1haWxfdmVyaWZpZWQiOnRydWV9LCJpYXQiOjE3NzAxNDcwNjEsImlzcyI6Imh0dHBzOi8vYXV0aC5vcGVuYWkuY29tIiwianRpIjoiOTU3NDI0YjgtNDRiNS00MTQ1LThlMGItNTkyZmY2M2QwY2UzIiwibmJmIjoxNzcwMTQ3MDYxLCJwd2RfYXV0aF90aW1lIjoxNzY1OTY2MTg1ODQ2LCJzY3AiOlsib3BlbmlkIiwicHJvZmlsZSIsImVtYWlsIiwib2ZmbGluZV9hY2Nlc3MiXSwic2Vzc2lvbl9pZCI6ImF1dGhzZXNzX2hNUmYzTkJuaEcyNWlMckg2cndFM2FabyIsInN1YiI6Imdvb2dsZS1vYXV0aDJ8MTAyOTQ5ODAyNTIzMzkwMjk4NTgxIn0.test_signature"

func TestExtractAccountIDFromJWT(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		wantID    string
		wantError bool
	}{
		{
			name:      "valid test token",
			token:     testToken,
			wantID:    "376f7e87-6a8b-4d8b-896e-cca5c1a9b35f",
			wantError: false,
		},
		{
			name:      "invalid token format - too few parts",
			token:     "invalid.token",
			wantID:    "",
			wantError: true,
		},
		{
			name:      "invalid token format - empty",
			token:     "",
			wantID:    "",
			wantError: true,
		},
		{
			name:      "invalid base64 payload",
			token:     "header.!!!invalid!!!.signature",
			wantID:    "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountID, err := extractAccountIDFromJWT(tt.token)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if accountID != tt.wantID {
					t.Errorf("got account ID %q, want %q", accountID, tt.wantID)
				}
			}
		})
	}
}

func TestConvertInstructions(t *testing.T) {
	client := &CodexClient{}

	tests := []struct {
		name    string
		prompts []string
		want    string
	}{
		{
			name:    "single prompt appends shared guidance for supported model",
			prompts: []string{"You are a helpful assistant."},
			want:    "You are a helpful assistant.\n\n" + openaidriver.OpenAIFamilyAgentGuidance(models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}),
		},
		{
			name:    "multiple prompts",
			prompts: []string{"First instruction.", "Second instruction.", "Third instruction."},
			want:    "First instruction.\n\nSecond instruction.\n\nThird instruction.\n\n" + openaidriver.OpenAIFamilyAgentGuidance(models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}),
		},
		{
			name:    "empty prompts filtered",
			prompts: []string{"First.", "", "  ", "Second."},
			want:    "First.\n\nSecond.\n\n" + openaidriver.OpenAIFamilyAgentGuidance(models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}),
		},
		{
			name:    "no prompts",
			prompts: []string{},
			want:    openaidriver.OpenAIFamilyAgentGuidance(models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}),
		},
		{
			name:    "unsupported model keeps prompts unchanged",
			prompts: []string{"First.", "Second."},
			want:    "First.\n\nSecond.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.name, "unsupported") {
				client.options.Model = models.Model{APIModel: "claude-opus-4-6"}
			} else {
				client.options.Model = models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}
			}
			got := client.convertInstructions(tt.prompts)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestConvertMessages validates that messages are converted to the correct SDK
// wire format by serializing to JSON and checking the structure.
func TestConvertMessages(t *testing.T) {
	client := &CodexClient{}

	tests := []struct {
		name     string
		messages []message.Message
		wantLen  int
		validate func(t *testing.T, items []map[string]interface{})
	}{
		{
			name: "user message",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "Hello, world!"},
					},
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, items []map[string]interface{}) {
				item := items[0]
				if item["role"] != "user" {
					t.Errorf("expected role 'user', got %v", item["role"])
				}
				if item["content"] != "Hello, world!" {
					t.Errorf("expected content 'Hello, world!', got %v", item["content"])
				}
			},
		},
		{
			name: "user message with image attachment",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "What is in this image?"},
						message.BinaryContent{Path: "/tmp/cat.png", MIMEType: "image/png", Data: []byte("img-bytes")},
					},
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, items []map[string]interface{}) {
				item := items[0]
				content, ok := item["content"].([]interface{})
				if !ok {
					t.Fatalf("expected content to be array, got %T (%v)", item["content"], item["content"])
				}
				if len(content) != 2 {
					t.Fatalf("expected 2 content parts, got %d", len(content))
				}
				first, ok := content[0].(map[string]interface{})
				if !ok {
					t.Fatalf("first content part type = %T, want map", content[0])
				}
				if first["type"] != "input_text" {
					t.Errorf("first content type = %v, want 'input_text'", first["type"])
				}
				if first["text"] != "What is in this image?" {
					t.Errorf("first content text = %v, want prompt text", first["text"])
				}
				second, ok := content[1].(map[string]interface{})
				if !ok {
					t.Fatalf("second content part type = %T, want map", content[1])
				}
				if second["type"] != "input_image" {
					t.Errorf("second content type = %v, want 'input_image'", second["type"])
				}
				if second["image_url"] != "data:image/png;base64,aW1nLWJ5dGVz" {
					t.Errorf("second content image_url = %v, want data URL", second["image_url"])
				}
			},
		},
		{
			name: "user message with non-image attachment fallback",
			messages: []message.Message{
				{
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "Please summarize the attachment"},
						message.BinaryContent{Path: "/tmp/doc.pdf", MIMEType: "application/pdf", Data: []byte("pdf-bytes")},
					},
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, items []map[string]interface{}) {
				item := items[0]
				content, ok := item["content"].([]interface{})
				if !ok {
					t.Fatalf("expected content to be array, got %T (%v)", item["content"], item["content"])
				}
				if len(content) != 2 {
					t.Fatalf("expected 2 content parts, got %d", len(content))
				}
				first, ok := content[0].(map[string]interface{})
				if !ok {
					t.Fatalf("first content part type = %T, want map", content[0])
				}
				if first["type"] != "input_text" {
					t.Errorf("first content type = %v, want 'input_text'", first["type"])
				}
				if first["text"] != "Please summarize the attachment" {
					t.Errorf("first content text = %v, want prompt text", first["text"])
				}
				second, ok := content[1].(map[string]interface{})
				if !ok {
					t.Fatalf("second content part type = %T, want map", content[1])
				}
				if second["type"] != "input_file" {
					t.Errorf("second content type = %v, want 'input_file'", second["type"])
				}
				if second["filename"] != "doc.pdf" {
					t.Errorf("second content filename = %v, want 'doc.pdf'", second["filename"])
				}
				expectedData := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("pdf-bytes"))
				if second["file_data"] != expectedData {
					t.Errorf("second content file_data = %v, want %v", second["file_data"], expectedData)
				}
			},
		},
		{
			name: "assistant message with tool calls",
			messages: []message.Message{
				{
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.TextContent{Text: "Let me help you."},
						message.ToolCall{
							ID:    "call_123",
							Name:  "get_weather",
							Input: `{"location": "NYC"}`,
						},
					},
				},
			},
			wantLen: 2, // One message + one function_call
			validate: func(t *testing.T, items []map[string]interface{}) {
				// First item: assistant text (sent as user role in SDK)
				if items[0]["content"] != "Let me help you." {
					t.Errorf("first item content = %v, want 'Let me help you.'", items[0]["content"])
				}
				// Second item: function_call
				if items[1]["type"] != "function_call" {
					t.Errorf("second item type = %v, want 'function_call'", items[1]["type"])
				}
				if items[1]["call_id"] != "call_123" {
					t.Errorf("second item call_id = %v, want 'call_123'", items[1]["call_id"])
				}
				if items[1]["name"] != "get_weather" {
					t.Errorf("second item name = %v, want 'get_weather'", items[1]["name"])
				}
			},
		},
		{
			name: "tool result",
			messages: []message.Message{
				{
					Role: message.Tool,
					Parts: []message.ContentPart{
						message.ToolResult{
							ToolCallID: "call_123",
							Content:    `{"temperature": 72}`,
						},
					},
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, items []map[string]interface{}) {
				item := items[0]
				if item["type"] != "function_call_output" {
					t.Errorf("expected type 'function_call_output', got %v", item["type"])
				}
				if item["call_id"] != "call_123" {
					t.Errorf("expected call_id 'call_123', got %v", item["call_id"])
				}
				if item["output"] != `{"temperature": 72}` {
					t.Errorf("expected output '{\"temperature\": 72}', got %v", item["output"])
				}
			},
		},
		{
			name: "system message becomes developer",
			messages: []message.Message{
				{
					Role: message.System,
					Parts: []message.ContentPart{
						message.TextContent{Text: "You are a coding assistant."},
					},
				},
			},
			wantLen: 1,
			validate: func(t *testing.T, items []map[string]interface{}) {
				item := items[0]
				if item["role"] != "developer" {
					t.Errorf("expected role 'developer', got %v", item["role"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.convertMessages(tt.messages)

			// Serialize to JSON to inspect the wire format
			b, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("failed to marshal result: %v", err)
			}

			var items []map[string]interface{}
			if err := json.Unmarshal(b, &items); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}

			if len(items) != tt.wantLen {
				t.Errorf("got %d items, want %d; items=%s", len(items), tt.wantLen, string(b))
			}
			if tt.validate != nil && len(items) >= tt.wantLen {
				tt.validate(t, items)
			}
		})
	}
}

func TestConvertMessages_SkipsBlankContent(t *testing.T) {
	client := &CodexClient{}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "   "},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "\n\t"},
			},
		},
		{
			Role: message.System,
			Parts: []message.ContentPart{
				message.TextContent{Text: ""},
			},
		},
	}

	result := client.convertMessages(messages)
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal converted messages: %v", err)
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatalf("failed to unmarshal converted messages: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected no input items for blank-only messages, got %d: %s", len(items), string(b))
	}
}

func TestBuildParams_RejectsEmptyConvertedInput(t *testing.T) {
	client := &CodexClient{}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "   "},
			},
		},
	}

	_, err := client.buildParams([]string{"system prompt"}, messages, nil)
	if err == nil {
		t.Fatal("expected buildParams to fail for empty converted input, got nil error")
	}

	if !strings.Contains(err.Error(), "codex request has no input items") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildParams_AllowsNonEmptyInput(t *testing.T) {
	client := &CodexClient{
		options: llm.DriverOptions{Model: models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}},
	}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "hello"},
			},
		},
	}

	params, err := client.buildParams([]string{"system prompt"}, messages, nil)
	if err != nil {
		t.Fatalf("expected buildParams to succeed for non-empty input, got error: %v", err)
	}

	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}

	jsonStr := string(b)
	if !strings.Contains(jsonStr, "\"input\"") {
		t.Fatalf("expected marshaled params to include input field, got: %s", jsonStr)
	}
	// json.Marshal escapes angle brackets, so search for the escaped form.
	if count := strings.Count(jsonStr, `\u003coutput_contract\u003e`); count != 1 {
		t.Fatalf("expected shared guidance exactly once in codex instructions, got %d: %s", count, jsonStr)
	}
}

func TestBuildParams_RejectsScopedToolNamesInPreflight(t *testing.T) {
	client := &CodexClient{}
	messages := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}

	_, err := client.buildParams([]string{"system prompt"}, messages, []llmtools.Tool{
		stubTool{name: "mcp__/tmp/worktree::chrome-devtools__fill"},
	})
	if err == nil {
		t.Fatal("expected buildParams to reject scoped tool name")
	}
	if !strings.Contains(err.Error(), "preflight rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildParams_PrunesDuplicateToolNamesDeterministically(t *testing.T) {
	client := &CodexClient{}
	messages := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}

	params, err := client.buildParams([]string{"system prompt"}, messages, []llmtools.Tool{
		stubTool{name: "z_tool"},
		stubTool{name: "a_tool"},
		stubTool{name: "z_tool"},
	})
	if err != nil {
		t.Fatalf("buildParams returned error: %v", err)
	}

	if len(params.Tools) != 2 {
		t.Fatalf("expected 2 unique tools after duplicate pruning, got %d", len(params.Tools))
	}

	first := params.Tools[0].OfFunction.Name
	second := params.Tools[1].OfFunction.Name
	if first != "a_tool" || second != "z_tool" {
		t.Fatalf("expected deterministic sorted order [a_tool z_tool], got [%s %s]", first, second)
	}
}

func TestBuildParams_RejectsToolNameLongerThan64(t *testing.T) {
	client := &CodexClient{}
	messages := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	longName := strings.Repeat("x", 65)

	_, err := client.buildParams([]string{"system prompt"}, messages, []llmtools.Tool{
		stubTool{name: longName},
	})
	if err == nil {
		t.Fatal("expected buildParams to reject tool name longer than 64")
	}
	if !strings.Contains(err.Error(), "exceeds codex max length of 64") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildParams_SparkOmitsReasoningSummaryAndInclude(t *testing.T) {
	client := &CodexClient{
		options: llm.DriverOptions{
			Model: models.Model{
				ID:                   models.GPT53CodexSpark,
				CanReason:            true,
				ReasoningSummaryMode: models.ReasoningSummaryDetailedOnly,
			},
			ReasoningEffort: "medium",
		},
	}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "hello"},
			},
		},
	}

	params, err := client.buildParams([]string{"system prompt"}, messages, nil)
	if err != nil {
		t.Fatalf("expected buildParams to succeed for spark, got error: %v", err)
	}

	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	jsonStr := string(b)

	if strings.Contains(jsonStr, "\"summary\"") {
		t.Fatalf("expected spark params to omit reasoning summary, got: %s", jsonStr)
	}
	if strings.Contains(jsonStr, "\"include\"") {
		t.Fatalf("expected spark params to omit include list, got: %s", jsonStr)
	}
}

func TestBuildParams_GPT54IncludesReasoningSummaryAndInclude(t *testing.T) {
	client := &CodexClient{
		options: llm.DriverOptions{
			Model: models.Model{
				ID:                   models.GPT54,
				APIModel:             "gpt-5.4",
				CanReason:            true,
				ReasoningSummaryMode: models.ReasoningSummaryAny,
			},
			ReasoningEffort: "xhigh",
		},
	}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "hello"},
			},
		},
	}

	params, err := client.buildParams([]string{"system prompt"}, messages, nil)
	if err != nil {
		t.Fatalf("expected buildParams to succeed for gpt-5.4, got error: %v", err)
	}

	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	jsonStr := string(b)

	if !strings.Contains(jsonStr, "\"summary\":\"concise\"") {
		t.Fatalf("expected gpt-5.4 params to include concise reasoning summary, got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, "\"include\"") {
		t.Fatalf("expected gpt-5.4 params to include include list, got: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, "\"model\":\"gpt-5.4\"") {
		t.Fatalf("expected gpt-5.4 model in params, got: %s", jsonStr)
	}
}

func TestBuildParams_GPT54ProRejected(t *testing.T) {
	client := &CodexClient{
		options: llm.DriverOptions{
			Model: models.Model{
				ID:       models.GPT54Pro,
				APIModel: "gpt-5.4-pro",
			},
		},
	}

	messages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "hello"},
			},
		},
	}

	_, err := client.buildParams([]string{"system prompt"}, messages, nil)
	if err == nil {
		t.Fatal("expected buildParams to reject gpt-5.4-pro on codex driver")
	}
	if !strings.Contains(err.Error(), "openai-only") {
		t.Fatalf("expected openai-only error, got: %v", err)
	}
}

func TestExtractUpstreamCorrelationHeaders(t *testing.T) {
	t.Run("returns empty values when response is nil", func(t *testing.T) {
		requestID, proxymanID := extractUpstreamCorrelationHeaders(nil)
		if requestID != "" || proxymanID != "" {
			t.Fatalf("expected empty correlation IDs for nil response, got requestID=%q proxymanID=%q", requestID, proxymanID)
		}
	})

	t.Run("reads correlation headers and trims whitespace", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("x-oai-request-id", "  req_123  ")
		resp.Header.Set("x-proxyman-id", "  flow_456  ")

		requestID, proxymanID := extractUpstreamCorrelationHeaders(resp)
		if requestID != "req_123" {
			t.Fatalf("requestID = %q, want req_123", requestID)
		}
		if proxymanID != "flow_456" {
			t.Fatalf("proxymanID = %q, want flow_456", proxymanID)
		}
	})
}

func TestNewClient_UsesApiKeyAsBearerToken(t *testing.T) {
	validToken := createTestJWT(time.Now().Add(1 * time.Hour).Unix())

	client, err := NewClient(llm.DriverOptions{ApiKey: validToken})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() returned nil client")
	}
	if client.accessToken != validToken {
		t.Fatalf("client.accessToken = %q, want provided API key token", client.accessToken)
	}
	if client.accessToken == "" {
		t.Fatal("expected accessToken to be set when ApiKey token is provided")
	}
	if client.accountID != "test-account-id" {
		t.Fatalf("client.accountID = %q, want %q", client.accountID, "test-account-id")
	}
}

func TestNewClient_PrefersBearerTokenOverApiKey(t *testing.T) {
	validToken := createTestJWT(time.Now().Add(1 * time.Hour).Unix())

	client, err := NewClient(llm.DriverOptions{
		ApiKey:      "not-a-jwt-token",
		BearerToken: validToken,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() returned nil client")
	}
	if client.accessToken != validToken {
		t.Fatalf("client.accessToken = %q, want bearer token", client.accessToken)
	}
	if client.accessToken == "" {
		t.Fatal("expected accessToken to be set when BearerToken is provided")
	}
	if client.accountID != "test-account-id" {
		t.Fatalf("client.accountID = %q, want %q", client.accountID, "test-account-id")
	}
}

// createTestJWT creates a JWT with the given expiry for testing
func createTestJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claims := map[string]interface{}{
		"exp": exp,
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": "test-account-id",
		},
	}
	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signature := base64.RawURLEncoding.EncodeToString([]byte("test_signature"))

	return header + "." + payload + "." + signature
}

func TestIsTokenExpired(t *testing.T) {
	tests := []struct {
		name        string
		expiryDelta time.Duration
		wantExpired bool
	}{
		{
			name:        "token expired in past",
			expiryDelta: -10 * time.Minute,
			wantExpired: true,
		},
		{
			name:        "token expires within buffer (5 min)",
			expiryDelta: 3 * time.Minute,
			wantExpired: true,
		},
		{
			name:        "token expires just at buffer boundary",
			expiryDelta: 5 * time.Minute,
			wantExpired: true,
		},
		{
			name:        "token valid beyond buffer",
			expiryDelta: 10 * time.Minute,
			wantExpired: false,
		},
		{
			name:        "token valid for 1 hour",
			expiryDelta: 1 * time.Hour,
			wantExpired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := time.Now().Add(tt.expiryDelta).Unix()
			token := createTestJWT(exp)

			got := IsTokenExpired(token)
			if got != tt.wantExpired {
				t.Errorf("IsTokenExpired() = %v, want %v", got, tt.wantExpired)
			}
		})
	}
}

func TestGetTokenExpiry(t *testing.T) {
	now := time.Now()
	exp := now.Add(1 * time.Hour).Unix()
	token := createTestJWT(exp)

	expiry, err := GetTokenExpiry(token)
	if err != nil {
		t.Fatalf("GetTokenExpiry() error = %v", err)
	}

	if expiry.Unix() != exp {
		t.Errorf("GetTokenExpiry() = %v, want %v", expiry.Unix(), exp)
	}
}

func TestExtractAccountIDFromAccessToken(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		wantID    string
		wantError bool
	}{
		{
			name:      "valid token with account ID",
			token:     testToken,
			wantID:    "376f7e87-6a8b-4d8b-896e-cca5c1a9b35f",
			wantError: false,
		},
		{
			name:      "invalid token format",
			token:     "invalid",
			wantID:    "",
			wantError: true,
		},
		{
			name:      "token missing account ID",
			token:     createTestJWTWithoutAccountID(),
			wantID:    "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := extractAccountIDFromAccessToken(tt.token)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if id != tt.wantID {
					t.Errorf("got ID %q, want %q", id, tt.wantID)
				}
			}
		})
	}
}

// createTestJWTWithoutAccountID creates a JWT without the account ID claim
func createTestJWTWithoutAccountID() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claims := map[string]interface{}{
		"exp": time.Now().Add(1 * time.Hour).Unix(),
	}
	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signature := base64.RawURLEncoding.EncodeToString([]byte("test_signature"))

	return header + "." + payload + "." + signature
}
