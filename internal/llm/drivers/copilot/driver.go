// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

type CopilotClient struct {
	options    llm.DriverOptions
	client     openai.Client
	httpClient *http.Client
}

// Name returns the name of the driver
func (c *CopilotClient) Name() string {
	return "copilot"
}

// CopilotTokenResponse represents the response from GitHub's token exchange endpoint
type CopilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// loadGitHubToken loads the GitHub OAuth token from the standard GitHub CLI/Copilot locations
func loadGitHubToken() (string, error) {
	// First check environment variable
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}

	// Get config directory
	var configDir string
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		configDir = xdgConfig
	} else if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			configDir = localAppData
		} else {
			configDir = filepath.Join(os.Getenv("HOME"), "AppData", "Local")
		}
	} else {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}

	// Try both hosts.json and apps.json files
	filePaths := []string{
		filepath.Join(configDir, "github-copilot", "hosts.json"),
		filepath.Join(configDir, "github-copilot", "apps.json"),
	}

	for _, filePath := range filePaths {
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var config map[string]map[string]interface{}
		if err := json.Unmarshal(data, &config); err != nil {
			continue
		}

		for key, value := range config {
			if strings.Contains(key, "github.com") {
				if oauthToken, ok := value["oauth_token"].(string); ok {
					return oauthToken, nil
				}
			}
		}
	}

	return "", fmt.Errorf("GitHub token not found in standard locations")
}

// exchangeGitHubToken exchanges a GitHub token for a Copilot bearer token
func (c *CopilotClient) exchangeGitHubToken(githubToken string) (string, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create token exchange request: %w", err)
	}

	req.Header.Set("Authorization", "Token "+githubToken)
	req.Header.Set("User-Agent", "Reliant/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to exchange GitHub token: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logging.Error("Failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp CopilotTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.Token, nil
}

func NewClient(opts llm.DriverOptions) (*CopilotClient, error) {
	if opts.ReasoningEffort == "" {
		opts.ReasoningEffort = "medium" // Default reasoning effort
	}

	// Create HTTP client for token exchange
	httpClient := llm.ResilientHTTPClient()
	httpClient.Timeout = 30 * time.Second

	var bearerToken string

	// If bearer token is already provided, use it
	if opts.BearerToken != "" {
		bearerToken = opts.BearerToken
	} else {
		// Try to get GitHub token from multiple sources
		var githubToken string

		// 1. Environment variable
		githubToken = os.Getenv("GITHUB_TOKEN")

		// 2. API key from options
		if githubToken == "" {
			githubToken = opts.ApiKey
		}

		// 3. Standard GitHub CLI/Copilot locations
		if githubToken == "" {
			var err error
			githubToken, err = loadGitHubToken()
			if err != nil {
				logging.Debug("Failed to load GitHub token from standard locations", "error", err)
			}
		}

		if githubToken == "" {
			logging.Error("GitHub token is required for Copilot provider. Set GITHUB_TOKEN environment variable, configure it in reliant.json, or ensure GitHub CLI/Copilot is properly authenticated.")
			return nil, fmt.Errorf("GitHub token is required")
		}

		// Create a temporary client for token exchange
		tempClient := &CopilotClient{
			options:    opts,
			httpClient: httpClient,
		}

		// Exchange GitHub token for bearer token
		var err error
		bearerToken, err = tempClient.exchangeGitHubToken(githubToken)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange GitHub token: %w", err)
		}
	}

	opts.BearerToken = bearerToken

	// GitHub Copilot API base URL
	baseURL := "https://api.githubcopilot.com"

	openaiClientOptions := []option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithAPIKey(bearerToken), // Use bearer token as API key
	}

	// Add GitHub Copilot specific headers
	openaiClientOptions = append(openaiClientOptions,
		option.WithHeader("Editor-Version", "Reliant/1.0"),
		option.WithHeader("Editor-Plugin-Version", "Reliant/1.0"),
		option.WithHeader("Copilot-Integration-Id", "vscode-chat"),
	)

	// Use streaming HTTP client with DNS resilience, ResponseHeaderTimeout,
	// and idle stream timeout for the OpenAI SDK client.
	openaiClientOptions = append(openaiClientOptions, option.WithHTTPClient(llm.StreamingHTTPClient()))

	// Add any extra headers
	if opts.ExtraHeaders != nil {
		for key, value := range opts.ExtraHeaders {
			openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
		}
	}

	client := openai.NewClient(openaiClientOptions...)
	// logging.Debug("Copilot client created", "opts", opts, "copilotOpts", copilotOpts, "model", opts.Model)
	return &CopilotClient{
		options:    opts,
		client:     client,
		httpClient: httpClient,
	}, nil
}

func (c *CopilotClient) convertMessages(prompts []string, messages []message.Message) (copilotMessages []openai.ChatCompletionMessageParamUnion) {
	// Add system message first
	for _, prompt := range prompts {
		if prompt != "" {
			copilotMessages = append(copilotMessages, openai.SystemMessage(prompt))
		}
	}

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam
			textBlock := openai.ChatCompletionContentPartTextParam{Text: msg.Content().String()}
			content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})

			for _, binaryContent := range msg.BinaryContent() {
				if isImageMimeType(binaryContent.MIMEType) {
					imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.String(Family)}
					imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}
					content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
				} else {
					fileData := "data:" + binaryContent.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(binaryContent.Data)
					filename := filepath.Base(binaryContent.Path)
					if filename == "" || filename == "." {
						filename = "file"
					}
					filePart := openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
						FileData: param.NewOpt(fileData),
						Filename: param.NewOpt(filename),
					})
					content = append(content, filePart)
				}
			}

			copilotMessages = append(copilotMessages, openai.UserMessage(content))

		case message.Assistant:
			assistantMsg := openai.ChatCompletionAssistantMessageParam{
				Role: "assistant",
			}

			// Always set content, even if empty (OpenAI API requires it)
			assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(msg.Content().String()),
			}

			if len(msg.ToolCalls()) > 0 {
				assistantMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallUnionParam, len(msg.ToolCalls()))
				for i, call := range msg.ToolCalls() {
					functionCall := openai.ChatCompletionMessageFunctionToolCallParam{
						ID:   call.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      call.Name,
							Arguments: call.Input,
						},
					}
					assistantMsg.ToolCalls[i] = openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &functionCall,
					}
				}
			}

			copilotMessages = append(copilotMessages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistantMsg,
			})

		case message.Tool:
			for _, result := range msg.ToolResults() {
				copilotMessages = append(copilotMessages,
					openai.ToolMessage(result.Content, result.ToolCallID),
				)
			}
		}
	}

	return
}

func (c *CopilotClient) convertTools(tools []toolsPkg.Tool) []openai.ChatCompletionToolUnionParam {
	copilotTools := make([]openai.ChatCompletionToolUnionParam, len(tools))

	for i, tool := range tools {
		schema := tool.ParamSchema()

		// Ensure required is always an array (even if empty) for OpenAI compatibility
		required := schema.Required
		if required == nil {
			required = []string{}
		}

		// Convert properties from OrderedMap to regular map for OpenAI
		properties := make(map[string]interface{})
		if schema.Properties != nil {
			for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
				properties[pair.Key] = pair.Value
			}
		}

		copilotTools[i] = openai.ChatCompletionFunctionTool(
			openai.FunctionDefinitionParam{
				Name:        tool.Name(),
				Description: openai.String(tool.Description()),
				Parameters: openai.FunctionParameters{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
			},
		)
	}

	return copilotTools
}

func (c *CopilotClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "stop":
		return message.FinishReasonEndTurn
	case "length":
		return message.FinishReasonMaxTokens
	case "tool_calls":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonUnknown
	}
}

func (c *CopilotClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.options.Model.APIModel),
		Messages: messages,
	}

	// Only set tools if there are any
	if len(tools) > 0 {
		params.Tools = tools
	}

	if c.options.Model.CanReason || c.options.Model.UseMaxCompletionTokens {
		params.MaxCompletionTokens = openai.Int(c.options.MaxTokens)
		if c.options.Model.CanReason {
			switch c.options.ReasoningEffort {
			case "low":
				params.ReasoningEffort = shared.ReasoningEffortLow
			case "medium":
				params.ReasoningEffort = shared.ReasoningEffortMedium
			case "high":
				params.ReasoningEffort = shared.ReasoningEffortHigh
			default:
				params.ReasoningEffort = shared.ReasoningEffortMedium
			}
		}
	} else {
		params.MaxTokens = openai.Int(c.options.MaxTokens)
	}

	return params
}

func (c *CopilotClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) (response *llm.DriverResponse, err error) {
	params := c.preparedParams(c.convertMessages(prompts, messages), c.convertTools(tools))
	isDebug := logging.GetLogLevel() == slog.LevelDebug
	if isDebug {
		// jsonData, _ := json.Marshal(params)
		// logging.Debug("Prepared messages", "messages", string(jsonData))
		// Deprecated: toolsPkg.SessionIDContextKey - use rctx.Context instead
		// if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok {
		// 	sessionId = sid
		// }
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	for {
		attempts++
		copilotResponse, err := c.client.Chat.Completions.New(
			ctx,
			params,
		)

		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := c.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.Warn(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, models.MaxRetries))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}

		content := ""
		if copilotResponse.Choices[0].Message.Content != "" {
			content = copilotResponse.Choices[0].Message.Content
		}

		toolCalls := c.toolCalls(*copilotResponse)
		finishReason := c.finishReason(string(copilotResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &llm.DriverResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        c.usage(*copilotResponse),
			FinishReason: finishReason,
		}, nil
	}
}

func (c *CopilotClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) <-chan llm.DriverEvent {
	params := c.preparedParams(c.convertMessages(prompts, messages), c.convertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	isDebug := logging.GetLogLevel() == slog.LevelDebug
	if isDebug {
		// Deprecated: toolsPkg.SessionIDContextKey - use rctx.Context instead
		// if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok {
		// 	sessionId = sid
		// }
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	eventChan := make(chan llm.DriverEvent)

	go func() {
		for {
			attempts++
			copilotStream := c.client.Chat.Completions.NewStreaming(
				ctx,
				params,
			)

			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCalls := make([]message.ToolCall, 0)

			for copilotStream.Next() {
				chunk := copilotStream.Current()
				acc.AddChunk(chunk)

				for _, choice := range chunk.Choices {
					if choice.Delta.Content != "" {
						eventChan <- llm.DriverEvent{
							Type:    llm.EventContentDelta,
							Content: choice.Delta.Content,
						}
						currentContent += choice.Delta.Content
					}
				}
			}

			err := copilotStream.Err()
			if err == nil || errors.Is(err, io.EOF) {
				// Stream completed successfully
				finishReason := c.finishReason(string(acc.ChatCompletion.Choices[0].FinishReason))
				if len(acc.Choices[0].Message.ToolCalls) > 0 {
					toolCalls = append(toolCalls, c.toolCalls(acc.ChatCompletion)...)
				}
				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				eventChan <- llm.DriverEvent{
					Type: llm.EventComplete,
					Response: &llm.DriverResponse{
						Content:      currentContent,
						ToolCalls:    toolCalls,
						Usage:        c.usage(acc.ChatCompletion),
						FinishReason: finishReason,
					},
				}
				close(eventChan)
				return
			}

			// If there is an error we are going to see if we can retry the call
			retry, after, retryErr := c.shouldRetry(attempts, err)
			if retryErr != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
				close(eventChan)
				return
			}
			// shouldRetry is not catching the max retries...
			// TODO: Figure out why
			if attempts > models.MaxRetries {
				logging.Warn("Maximum retry attempts reached for rate limit", "attempts", attempts, "max_retries", models.MaxRetries)
				retry = false
			}
			if retry {
				logging.Warn(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d (paused for %d ms)", attempts, models.MaxRetries, after))
				select {
				case <-ctx.Done():
					// context cancelled
					if ctx.Err() == nil {
						eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
					}
					close(eventChan)
					return
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
			close(eventChan)
			return
		}
	}()

	return eventChan
}

func (c *CopilotClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}

	// Check for token expiration (401 Unauthorized)
	if apierr.StatusCode == 401 {
		// Try to refresh the bearer token
		var githubToken string

		// 1. Environment variable
		githubToken = os.Getenv("GITHUB_TOKEN")

		// 2. API key from options
		if githubToken == "" {
			githubToken = c.options.ApiKey
		}

		// 3. Standard GitHub CLI/Copilot locations
		if githubToken == "" {
			var err error
			githubToken, err = loadGitHubToken()
			if err != nil {
				logging.Debug("Failed to load GitHub token from standard locations during retry", "error", err)
			}
		}

		if githubToken != "" {
			newBearerToken, tokenErr := c.exchangeGitHubToken(githubToken)
			if tokenErr == nil {
				c.options.BearerToken = newBearerToken
				// Update the client with the new token
				// Note: This is a simplified approach. In a production system,
				// you might want to recreate the entire client with the new token
				logging.Info("Refreshed Copilot bearer token")
				return true, 1000, nil // Retry immediately with new token
			}
			logging.Error("Failed to refresh Copilot bearer token", "error", tokenErr)
		}
		return false, 0, fmt.Errorf("authentication failed: %w", err)
	}
	logging.Debug("Copilot API Error", "status", apierr.StatusCode, "headers", apierr.Response.Header, "body", apierr.RawJSON())

	if apierr.StatusCode != 429 && apierr.StatusCode != 500 {
		return false, 0, err
	}

	if apierr.StatusCode == 500 {
		logging.Warn("Copilot API returned 500 error, retrying", "error", err)
	}

	if attempts > models.MaxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", models.MaxRetries)
	}

	retryMs := 0
	retryAfterValues := apierr.Response.Header.Values("Retry-After")

	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs = backoffMs + jitterMs
	if len(retryAfterValues) > 0 {
		if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
			retryMs = retryMs * 1000
		}
	}
	return true, int64(retryMs), nil
}

func (c *CopilotClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			// Skip empty or invalid tool calls
			if call.ID == "" || call.Function.Name == "" {
				logging.Warn("Skipping empty/invalid tool call",
					"id", call.ID,
					"name", call.Function.Name)
				continue
			}

			toolCall := message.ToolCall{
				ID:       call.ID,
				Name:     call.Function.Name,
				Input:    call.Function.Arguments,
				Type:     "function",
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (c *CopilotClient) usage(completion openai.ChatCompletion) llm.TokenUsage {
	// TokenCount = total tokens (prompt + completion)
	return llm.TokenUsage{
		TokenCount: completion.Usage.TotalTokens,
	}
}

func (c *CopilotClient) Model() models.Model {
	return c.options.Model
}

func (c *CopilotClient) ValidateKey(ctx context.Context) error {
	// Use GPT-5.4 Mini for validation (small, fast model)
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	// Create a temporary client with the small model
	validationOpts := c.options
	registry := models.MustGetRegistry()
	if def, ok := registry.GetDefinition(string(models.GPT54Mini)); ok {
		validationOpts.Model = def.ToModel()
	}
	validationOpts.MaxTokens = 10

	_, err := c.SendMessages(ctx, []string{}, testMessages, []toolsPkg.Tool{})
	return err
}

// isImageMimeType checks if a MIME type represents an image
func isImageMimeType(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/svg+xml":
		return true
	default:
		return false
	}
}
