// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: one pure function folding an llm.Driver event stream into a response; the driver it reads is already the llm.Driver seam
package accumulator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
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
//
// A failed stream still returns what it produced. When err is non-nil the
// response may be non-nil too: it carries the usage a final event reported
// (a provider can bill a request and then drop the connection, and drivers send
// that final event after the error as well as before it) and whatever content
// and tool calls had streamed. Callers must check err first and treat the
// response as partial; its only legitimate use on error is accounting for the
// tokens the request consumed.
func StreamAndAccumulate(
	ctx context.Context,
	driver llm.Driver,
	prompts []string,
	messages []message.Message,
	tools []tools.Tool,
) (*llm.DriverResponse, error) {
	streamStart := time.Now()

	logging.DebugContext(ctx, "[ACCUMULATOR] Starting streaming with accumulation",
		"driver", driver.Name(),
		"promptCount", len(prompts),
		"messageCount", len(messages),
		"toolCount", len(tools))

	// Trim messages if they would exceed context window limits
	// This prevents API errors from context overflow. The backstop threshold is
	// derived from the driver model's prompt ceiling (~95% of it), so it scales
	// per-model and sits above the compaction threshold rather than using a
	// fixed 200k-window assumption.
	model := driver.Model()
	ceiling := int64(models.PromptCeiling(int(model.ContextWindow), int(model.DefaultMaxTokens)))
	if message.TrimMessagesToFitContextWindow(messages, nil, nil, ceiling) {
		logging.DebugContext(ctx, "[ACCUMULATOR] Trimmed messages to fit context window",
			"promptCeiling", ceiling)
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
				logging.DebugContext(ctx, "[ACCUMULATOR] Not in activity context, skipping heartbeat")
			}
		}()
		activity.RecordHeartbeat(ctx, "streaming")
		logging.DebugContext(ctx, "[ACCUMULATOR] Heartbeat sent")
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
				// Fallback only — see the EventComplete merge below. Drivers
				// that report tool calls exclusively through stop events
				// (openrouter, mock_static, replay) depend on this.
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
				logging.ErrorContext(ctx, "[ACCUMULATOR] Stream error", "error", event.Error)

			case llm.EventContentStart, llm.EventToolUseStart, llm.EventContentStop:
				// Lifecycle events - just track for logging
				// Don't need to do anything special

			case llm.EventThinkingDelta:
				// Extended thinking - we don't need to accumulate this for compaction
				// but we log it for debugging
				if event.Thinking != "" {
					logging.DebugContext(ctx, "[ACCUMULATOR] Thinking delta", "length", len(event.Thinking))
				}

			default:
				logging.WarnContext(ctx, "[ACCUMULATOR] Unknown event type", "type", event.Type)
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
		return partialResponse(finalResponse, contentBuilder.String(), toolCalls), fmt.Errorf("streaming failed: %w", streamError)
	}

	// Check if we got a final response
	if finalResponse == nil {
		observability.LLMRequestsTotal.WithLabelValues(provider, "error").Inc()
		observability.LLMStreamDuration.WithLabelValues(provider).Observe(duration)
		observability.LLMRequestDuration.WithLabelValues(provider).Observe(duration)
		return partialResponse(nil, contentBuilder.String(), toolCalls), fmt.Errorf("stream ended without final response (processed %d events)", eventCount)
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

	// The final response's tool calls WIN over the ones seen mid-stream.
	//
	// A stop event marks a content block closing, and does not have to carry
	// the block's arguments: Anthropic streams those as separate
	// input_json_delta chunks, so its stop event names only the tool call id.
	// Overwriting the final response with those stubs produced calls whose
	// Input was the empty string, and every caller that unmarshalled one got
	// "unexpected end of JSON input" — deterministically, so retries could not
	// help. Only fall back to the mid-stream calls when the driver reported
	// none at the end.
	if len(finalResponse.ToolCalls) == 0 && len(toolCalls) > 0 {
		finalResponse.ToolCalls = toolCalls
	}

	return finalResponse, nil
}

// partialResponse is what a failed stream produced: the final event's response
// (and so its usage) when one arrived, with the content and tool calls seen
// while streaming. It is nil when the stream produced nothing at all.
func partialResponse(final *llm.DriverResponse, content string, toolCalls []message.ToolCall) *llm.DriverResponse {
	if final == nil && content == "" && len(toolCalls) == 0 {
		return nil
	}
	out := &llm.DriverResponse{}
	if final != nil {
		*out = *final
	}
	if content != "" {
		out.Content = content
	}
	if len(out.ToolCalls) == 0 && len(toolCalls) > 0 {
		out.ToolCalls = toolCalls
	}
	return out
}
