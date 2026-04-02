// Copyright (c) 2025 Reliant Labs
package accumulator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/observability"
)

// StreamAndAccumulate calls the driver's StreamResponse and accumulates all chunks
// into a single DriverResponse. This is useful for long-running LLM calls that
// require streaming mode but don't need real-time chunk processing.
//
// The function handles:
// - Content accumulation across all chunks
// - Tool call accumulation
// - Error handling from streaming events
// - Final response construction with usage stats
func StreamAndAccumulate(
	ctx context.Context,
	driver llm.Driver,
	prompts []string,
	messages []message.Message,
	tools []tools.Tool,
) (*llm.DriverResponse, error) {
	streamStart := time.Now()

	logging.Debug("[ACCUMULATOR] Starting streaming with accumulation",
		"driver", driver.Name(),
		"promptCount", len(prompts),
		"messageCount", len(messages),
		"toolCount", len(tools))

	// Trim messages if they would exceed context window limits
	// This prevents API errors from context overflow
	if message.TrimMessagesToFitContextWithFullEstimate(messages, nil, nil) {
		logging.Info("[ACCUMULATOR] Trimmed messages to fit context window")
	}

	// Accumulate content and tool calls
	var contentBuilder strings.Builder
	var toolCalls []message.ToolCall
	var finalResponse *llm.DriverResponse
	var streamError error

	// Start heartbeat ticker for long-running operations
	// This prevents Temporal from timing out during long LLM calls
	heartbeatTicker := time.NewTicker(10 * time.Second)
	defer heartbeatTicker.Stop()

	// Helper function to send heartbeat (handles non-activity context)
	sendHeartbeat := func() {
		defer func() {
			if r := recover(); r != nil {
				// Not in activity context (e.g., tests), ignore
				logging.Debug("[ACCUMULATOR] Not in activity context, skipping heartbeat")
			}
		}()
		activity.RecordHeartbeat(ctx, "streaming")
		logging.Debug("[ACCUMULATOR] Heartbeat sent")
	}

	// Get streaming channel
	eventChan := driver.StreamResponse(ctx, prompts, messages, tools)
	eventCount := 0

	// Process events with periodic heartbeats
	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				// Channel closed, streaming complete
				goto StreamingDone
			}

			eventCount++

			switch event.Type {
			case llm.EventContentDelta:
				// Accumulate text content
				if event.Content != "" {
					contentBuilder.WriteString(event.Content)
				}

			case llm.EventToolUseStop:
				// Tool call completed, add to list
				if event.ToolCall != nil {
					toolCalls = append(toolCalls, *event.ToolCall)
				}

			case llm.EventComplete:
				// Final event with usage stats
				if event.Response != nil {
					finalResponse = event.Response
				}

			case llm.EventError:
				// Error during streaming
				streamError = event.Error
				logging.Error("[ACCUMULATOR] Stream error", "error", event.Error)

			case llm.EventContentStart, llm.EventToolUseStart, llm.EventContentStop:
				// Lifecycle events - just track for logging
				// Don't need to do anything special

			case llm.EventThinkingDelta:
				// Extended thinking - we don't need to accumulate this for compaction
				// but we log it for debugging
				if event.Thinking != "" {
					logging.Debug("[ACCUMULATOR] Thinking delta", "length", len(event.Thinking))
				}

			default:
				logging.Warn("[ACCUMULATOR] Unknown event type", "type", event.Type)
			}

		case <-heartbeatTicker.C:
			// Send periodic heartbeat to keep activity alive
			sendHeartbeat()
		}
	}

StreamingDone:

	duration := time.Since(streamStart).Seconds()
	provider := driver.Name()

	// Check if streaming ended with an error
	if streamError != nil {
		observability.LLMRequestsTotal.WithLabelValues(provider, "error").Inc()
		observability.LLMStreamDuration.WithLabelValues(provider).Observe(duration)
		observability.LLMRequestDuration.WithLabelValues(provider).Observe(duration)
		return nil, fmt.Errorf("streaming failed: %w", streamError)
	}

	// Check if we got a final response
	if finalResponse == nil {
		observability.LLMRequestsTotal.WithLabelValues(provider, "error").Inc()
		observability.LLMStreamDuration.WithLabelValues(provider).Observe(duration)
		observability.LLMRequestDuration.WithLabelValues(provider).Observe(duration)
		return nil, fmt.Errorf("stream ended without final response (processed %d events)", eventCount)
	}

	// Record request metrics
	observability.LLMRequestsTotal.WithLabelValues(provider, "success").Inc()
	observability.LLMStreamDuration.WithLabelValues(provider).Observe(duration)
	observability.LLMRequestDuration.WithLabelValues(provider).Observe(duration)

	// Record token usage metrics with input/output split
	if finalResponse.Usage.InputTokens > 0 {
		observability.LLMTokensTotal.WithLabelValues(provider, "input").Add(float64(finalResponse.Usage.InputTokens))
	}
	if finalResponse.Usage.OutputTokens > 0 {
		observability.LLMTokensTotal.WithLabelValues(provider, "output").Add(float64(finalResponse.Usage.OutputTokens))
	}

	// Build the complete response
	accumulatedContent := contentBuilder.String()

	// Use the content from accumulated chunks if available, otherwise use finalResponse.Content
	// Some drivers might populate content in the final event instead of deltas
	if accumulatedContent != "" {
		finalResponse.Content = accumulatedContent
	}

	// Add accumulated tool calls if any
	if len(toolCalls) > 0 {
		finalResponse.ToolCalls = toolCalls
	}

	return finalResponse, nil
}