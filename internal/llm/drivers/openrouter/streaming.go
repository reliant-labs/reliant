// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// streamWithCacheControl implements streaming for Anthropic models with cache control
func (c *Client) streamWithCacheControl(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		// Convert messages with cache control
		convertedMessages := c.convertMessagesWithCacheControl(prompts, messages)

		// Convert tools with cache control for the last tool
		var convertedTools []map[string]interface{}
		if len(toolList) > 0 {
			openaiTools := c.ConvertTools(toolList)
			convertedTools = make([]map[string]interface{}, len(openaiTools))

			for i, tool := range openaiTools {
				// Convert tool to map via JSON round-trip
				toolJSON, _ := json.Marshal(tool)
				var toolMap map[string]interface{}
				_ = json.Unmarshal(toolJSON, &toolMap) //nolint:errcheck // Round-trip marshal/unmarshal always succeeds

				// Cache the last tool
				if i == len(toolList)-1 && !c.Options.DisableCache {
					if function, ok := toolMap["function"].(map[string]interface{}); ok {
						function["cache_control"] = map[string]string{
							"type": "ephemeral",
						}
					}
				}

				convertedTools[i] = toolMap
			}
		}

		// Build request
		request := map[string]interface{}{
			"model":       c.Options.Model.APIModel,
			"messages":    convertedMessages,
			"temperature": c.Options.Temperature,
			"max_tokens":  c.Options.MaxTokens,
			"stream":      true,
			"stream_options": map[string]interface{}{
				"include_usage": true,
			},
		}

		if len(convertedTools) > 0 {
			request["tools"] = convertedTools
			if choice := forcedToolChoice(c.Options.ForceToolChoice, convertedTools); choice != nil {
				request["tool_choice"] = choice
			}
		}

		requestBody, err := json.Marshal(request)
		if err != nil {
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		logging.Debug("OpenRouter streaming request with cache control",
			"model", request["model"],
			"hasTools", len(convertedTools) > 0,
			"messageCount", len(convertedMessages.([]map[string]interface{})))

		// Connect with retry logic for transient errors
		streamClient := llm.StreamingHTTPClient()
		var resp *http.Response
		for attempt := 1; ; attempt++ {
			httpReq, err := c.newOpenRouterRequest(ctx, requestBody)
			if err != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to create request: %w", err)}
				return
			}

			resp, err = streamClient.Do(httpReq)
			if err != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to send request: %w", err)}
				return
			}

			if resp.StatusCode == http.StatusOK {
				break
			}

			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if !isRetryableStatus(resp.StatusCode) || attempt > models.MaxRetries {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))}
				return
			}

			delay := retryDelay(attempt, resp)
			logging.Warn("OpenRouter retrying streaming request",
				"status", resp.StatusCode,
				"attempt", attempt,
				"maxRetries", models.MaxRetries,
				"delay", delay)
			select {
			case <-ctx.Done():
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
				return
			case <-time.After(delay):
				continue
			}
		}
		defer resp.Body.Close()

		// Monitor context cancellation and close resp.Body to unblock scanner
		go func() {
			<-ctx.Done()
			resp.Body.Close()
		}()

		// Process SSE stream
		scanner := bufio.NewScanner(resp.Body)
		var currentContent strings.Builder
		// Track tool calls by index to handle multiple concurrent calls
		toolCallsByIndex := make([]*message.ToolCall, 0)
		var allToolCalls []message.ToolCall
		var streamUsage llm.TokenUsage

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")

				// Skip the [DONE] message
				if data == "[DONE]" {
					break
				}

				// Parse JSON
				var event map[string]interface{}
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					logging.Debug("Failed to parse SSE event", "error", err, "data", data)
					continue
				}

				// Parse usage from final chunk (when stream_options.include_usage is set)
				if usage, ok := event["usage"].(map[string]interface{}); ok {
					if pt, ok := usage["prompt_tokens"].(float64); ok {
						streamUsage.InputTokens = int64(pt)
					}
					if ct, ok := usage["completion_tokens"].(float64); ok {
						streamUsage.OutputTokens = int64(ct)
					}
					if tt, ok := usage["total_tokens"].(float64); ok {
						streamUsage.TokenCount = int64(tt)
					}
				}

				// Process the event
				if choices, ok := event["choices"].([]interface{}); ok && len(choices) > 0 {
					choice := choices[0].(map[string]interface{})

					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// Content delta
						if content, ok := delta["content"].(string); ok {
							currentContent.WriteString(content)
							eventChan <- llm.DriverEvent{
								Type:    llm.EventContentDelta,
								Content: content,
							}
						}

						// Tool calls
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
							for _, tc := range toolCalls {
								toolCallMap := tc.(map[string]interface{})

								// Get the index - OpenAI uses this to identify which tool call in parallel calls
								var index int
								if idx, ok := toolCallMap["index"].(float64); ok {
									index = int(idx)
								} else {
									// If no index, treat as index 0 (single tool call)
									index = 0
								}

								// Ensure we have enough space in the slice
								for len(toolCallsByIndex) <= index {
									toolCallsByIndex = append(toolCallsByIndex, nil)
								}

								// Get or create the tool call at this index
								currentToolCall := toolCallsByIndex[index]
								if currentToolCall == nil {
									// New tool call at this index
									toolCallID, _ := toolCallMap["id"].(string)
									currentToolCall = &message.ToolCall{
										ID: toolCallID,
									}
									toolCallsByIndex[index] = currentToolCall

									if function, ok := toolCallMap["function"].(map[string]interface{}); ok {
										if name, ok := function["name"].(string); ok {
											currentToolCall.Name = name
										}
									}

									eventChan <- llm.DriverEvent{
										Type:     llm.EventToolUseStart,
										ToolCall: currentToolCall,
									}
								}

								// Append arguments
								if function, ok := toolCallMap["function"].(map[string]interface{}); ok {
									if args, ok := function["arguments"].(string); ok {
										currentToolCall.Input += args
										eventChan <- llm.DriverEvent{
											Type: llm.EventToolUseDelta,
											ToolCall: &message.ToolCall{
												ID:    currentToolCall.ID,
												Input: args,
											},
										}
									}
								}
							}
						}
					}

					// Check for finish reason
					if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
						// Finish all pending tool calls
						for _, toolCall := range toolCallsByIndex {
							if toolCall != nil {
								toolCall.Finished = true
								allToolCalls = append(allToolCalls, *toolCall)
								eventChan <- llm.DriverEvent{
									Type:     llm.EventToolUseStop,
									ToolCall: toolCall,
								}
							}
						}
						// Clear the slice for next message
						toolCallsByIndex = make([]*message.ToolCall, 0)

						// Send complete event
						eventChan <- llm.DriverEvent{
							Type: llm.EventComplete,
							Response: &llm.DriverResponse{
								Content:      currentContent.String(),
								ToolCalls:    allToolCalls,
								FinishReason: mapFinishReason(finishReason),
								Usage:        streamUsage,
							},
						}
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			// Don't report error if context was cancelled - that's expected
			if ctx.Err() == nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("error reading stream: %w", err)}
			} else {
				logging.Debug("OpenRouter stream cancelled", "ctxErr", ctx.Err())
			}
		}
	}()

	return eventChan
}

// streamWithGeminiSupport implements streaming for Gemini models with reasoning_details support
// Gemini 3.x models require thought signatures to be preserved for tool calls
func (c *Client) streamWithGeminiSupport(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		// Convert messages with Gemini-specific handling (includes reasoning_details)
		convertedMessages := c.convertMessagesForGemini(prompts, messages)

		// Convert tools to OpenAI format
		var convertedTools []map[string]interface{}
		if len(toolList) > 0 {
			openaiTools := c.ConvertTools(toolList)
			convertedTools = make([]map[string]interface{}, len(openaiTools))
			for i, tool := range openaiTools {
				toolJSON, _ := json.Marshal(tool)
				var toolMap map[string]interface{}
				_ = json.Unmarshal(toolJSON, &toolMap) //nolint:errcheck // Round-trip marshal/unmarshal always succeeds
				convertedTools[i] = toolMap
			}
		}

		// Build request
		request := map[string]interface{}{
			"model":       c.Options.Model.APIModel,
			"messages":    convertedMessages,
			"temperature": c.Options.Temperature,
			"max_tokens":  c.Options.MaxTokens,
			"stream":      true,
			"stream_options": map[string]interface{}{
				"include_usage": true,
			},
		}

		if len(convertedTools) > 0 {
			request["tools"] = convertedTools
			if choice := forcedToolChoice(c.Options.ForceToolChoice, convertedTools); choice != nil {
				request["tool_choice"] = choice
			}
		}

		// Add reasoning config for Gemini thinking models
		if c.Options.ReasoningEffort != "" && c.Options.ReasoningEffort != "disabled" {
			request["reasoning"] = map[string]string{
				"effort": c.Options.ReasoningEffort,
			}
			logging.Debug("OpenRouter Gemini streaming with reasoning",
				"effort", c.Options.ReasoningEffort)
		}

		requestBody, err := json.Marshal(request)
		if err != nil {
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		logging.Debug("OpenRouter Gemini streaming request",
			"model", request["model"],
			"hasTools", len(convertedTools) > 0,
			"messageCount", len(convertedMessages),
			"hasReasoning", request["reasoning"] != nil)

		// Connect with retry logic for transient errors
		streamClient := llm.StreamingHTTPClient()
		var resp *http.Response
		for attempt := 1; ; attempt++ {
			httpReq, err := c.newOpenRouterRequest(ctx, requestBody)
			if err != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to create request: %w", err)}
				return
			}

			resp, err = streamClient.Do(httpReq)
			if err != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to send request: %w", err)}
				return
			}

			if resp.StatusCode == http.StatusOK {
				break
			}

			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if !isRetryableStatus(resp.StatusCode) || attempt > models.MaxRetries {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))}
				return
			}

			delay := retryDelay(attempt, resp)
			logging.Warn("OpenRouter retrying Gemini streaming request",
				"status", resp.StatusCode,
				"attempt", attempt,
				"maxRetries", models.MaxRetries,
				"delay", delay)
			select {
			case <-ctx.Done():
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
				return
			case <-time.After(delay):
				continue
			}
		}
		defer resp.Body.Close()

		// Monitor context cancellation and close resp.Body to unblock scanner
		go func() {
			<-ctx.Done()
			resp.Body.Close()
		}()

		// Process SSE stream
		scanner := bufio.NewScanner(resp.Body)
		var currentContent strings.Builder
		// Track tool calls by index to handle multiple concurrent calls
		toolCallsByIndex := make([]*message.ToolCall, 0)
		var allToolCalls []message.ToolCall
		var streamUsage llm.TokenUsage
		// Track reasoning_details for Gemini thought signatures
		reasoningByID := make(map[string]ReasoningDetail)

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")

				// Skip the [DONE] message
				if data == "[DONE]" {
					break
				}

				// Parse JSON
				var event map[string]interface{}
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					logging.Debug("Failed to parse SSE event", "error", err, "data", data)
					continue
				}

				// Parse usage from final chunk (when stream_options.include_usage is set)
				if usage, ok := event["usage"].(map[string]interface{}); ok {
					if pt, ok := usage["prompt_tokens"].(float64); ok {
						streamUsage.InputTokens = int64(pt)
					}
					if ct, ok := usage["completion_tokens"].(float64); ok {
						streamUsage.OutputTokens = int64(ct)
					}
					if tt, ok := usage["total_tokens"].(float64); ok {
						streamUsage.TokenCount = int64(tt)
					}
				}

				// Process the event
				if choices, ok := event["choices"].([]interface{}); ok && len(choices) > 0 {
					choice := choices[0].(map[string]interface{})

					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// Content delta
						if content, ok := delta["content"].(string); ok {
							currentContent.WriteString(content)
							eventChan <- llm.DriverEvent{
								Type:    llm.EventContentDelta,
								Content: content,
							}
						}

						// Parse reasoning_details FIRST for Gemini thought signatures
						// They come in the same chunk as tool_calls and must be parsed before
						if rdList, ok := delta["reasoning_details"].([]interface{}); ok {
							for _, rdRaw := range rdList {
								if rdMap, ok := rdRaw.(map[string]interface{}); ok {
									rd := ReasoningDetail{}
									if t, ok := rdMap["type"].(string); ok {
										rd.Type = t
									}
									if id, ok := rdMap["id"].(string); ok {
										rd.ID = id
									}
									if data, ok := rdMap["data"].(string); ok {
										rd.Data = data
									}
									if rd.ID != "" {
										reasoningByID[rd.ID] = rd
										logging.Debug("OpenRouter Gemini streaming reasoning detail",
											"id", rd.ID,
											"type", rd.Type,
											"hasData", rd.Data != "")
									}
								}
							}
						}

						// Tool calls - process AFTER reasoning_details so we can associate them
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
							for _, tc := range toolCalls {
								toolCallMap := tc.(map[string]interface{})

								// Get the index
								var index int
								if idx, ok := toolCallMap["index"].(float64); ok {
									index = int(idx)
								}

								// Ensure we have enough space in the slice
								for len(toolCallsByIndex) <= index {
									toolCallsByIndex = append(toolCallsByIndex, nil)
								}

								// Get or create the tool call at this index
								currentToolCall := toolCallsByIndex[index]
								if currentToolCall == nil {
									toolCallID, _ := toolCallMap["id"].(string)
									currentToolCall = &message.ToolCall{
										ID: toolCallID,
									}
									toolCallsByIndex[index] = currentToolCall

									if function, ok := toolCallMap["function"].(map[string]interface{}); ok {
										if name, ok := function["name"].(string); ok {
											currentToolCall.Name = name
										}
									}

									// CRITICAL: Associate thought signature immediately when creating the tool call
									// The reasoning_details are in the same chunk with matching ID
									if rd, ok := reasoningByID[toolCallID]; ok {
										if rd.Type == "reasoning.encrypted" && rd.Data != "" {
											currentToolCall.ThoughtSignature = rd.Data
											logging.Debug("OpenRouter Gemini stream associated thought signature on create",
												"toolCallID", toolCallID,
												"toolName", currentToolCall.Name,
												"signatureLength", len(rd.Data))
										}
									}

									eventChan <- llm.DriverEvent{
										Type:     llm.EventToolUseStart,
										ToolCall: currentToolCall,
									}
								}

								// Append arguments
								if function, ok := toolCallMap["function"].(map[string]interface{}); ok {
									if args, ok := function["arguments"].(string); ok {
										currentToolCall.Input += args
										eventChan <- llm.DriverEvent{
											Type: llm.EventToolUseDelta,
											ToolCall: &message.ToolCall{
												ID:    currentToolCall.ID,
												Input: args,
											},
										}
									}
								}
							}
						}
					}

					// Check for finish reason
					if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
						// Finish all pending tool calls and associate thought signatures
						for _, toolCall := range toolCallsByIndex {
							if toolCall != nil {
								toolCall.Finished = true

								// Associate reasoning_details (thought signature) with tool call
								if rd, ok := reasoningByID[toolCall.ID]; ok {
									if rd.Type == "reasoning.encrypted" && rd.Data != "" {
										toolCall.ThoughtSignature = rd.Data
										logging.Debug("OpenRouter Gemini stream associated thought signature",
											"toolCallID", toolCall.ID,
											"toolName", toolCall.Name,
											"signatureLength", len(rd.Data))
									}
								}

								allToolCalls = append(allToolCalls, *toolCall)
								eventChan <- llm.DriverEvent{
									Type:     llm.EventToolUseStop,
									ToolCall: toolCall,
								}
							}
						}
						// Clear the slice for next message
						toolCallsByIndex = make([]*message.ToolCall, 0)

						// Send complete event
						eventChan <- llm.DriverEvent{
							Type: llm.EventComplete,
							Response: &llm.DriverResponse{
								Content:      currentContent.String(),
								ToolCalls:    allToolCalls,
								FinishReason: mapFinishReason(finishReason),
								Usage:        streamUsage,
							},
						}
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			// Don't report error if context was cancelled - that's expected
			if ctx.Err() == nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("error reading stream: %w", err)}
			} else {
				logging.Debug("OpenRouter Gemini stream cancelled", "ctxErr", ctx.Err())
			}
		}
	}()

	return eventChan
}
