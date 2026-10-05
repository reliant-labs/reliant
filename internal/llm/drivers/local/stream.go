// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

// serverReasoningEffort maps a Reliant thinking level to what local servers
// accept for reasoning_effort. Anything else is omitted rather than sent: a
// server that does not take the level answers 400 (Ollama: `think value "high"
// is not supported for this model`).
func serverReasoningEffort(level string) shared.ReasoningEffort {
	switch level {
	case "low", "medium", "high":
		return shared.ReasoningEffort(level)
	}
	return ""
}

// thinkSplitter separates an inline <think>…</think> block from answer text
// as it streams. Ollama's OpenAI endpoint returns qwen3-style reasoning inside
// `content` (its native API separates it; /v1 does not). Only a block at the
// very start of the response is treated as reasoning, so an answer that merely
// mentions the tag is left alone.
type thinkSplitter struct {
	state int
	buf   string
}

const (
	thinkStart = iota
	thinkInside
	thinkAfter
	thinkPassthrough
)

func (t *thinkSplitter) push(chunk string) (content, thinking string) {
	t.buf += chunk
	for {
		switch t.state {
		case thinkStart:
			trimmed := strings.TrimLeft(t.buf, " \t\r\n")
			switch {
			case trimmed == "":
				return content, thinking
			case strings.HasPrefix(trimmed, thinkOpenTag):
				t.buf = trimmed[len(thinkOpenTag):]
				t.state = thinkInside
			case strings.HasPrefix(thinkOpenTag, trimmed):
				return content, thinking // could still become the tag
			default:
				t.state = thinkPassthrough
			}
		case thinkInside:
			if i := strings.Index(t.buf, thinkCloseTag); i >= 0 {
				thinking += t.buf[:i]
				t.buf = t.buf[i+len(thinkCloseTag):]
				t.state = thinkAfter
				continue
			}
			hold := 0
			for n := min(len(thinkCloseTag)-1, len(t.buf)); n > 0; n-- {
				if strings.HasSuffix(t.buf, thinkCloseTag[:n]) {
					hold = n
					break
				}
			}
			thinking += t.buf[:len(t.buf)-hold]
			t.buf = t.buf[len(t.buf)-hold:]
			return content, thinking
		case thinkAfter:
			t.buf = strings.TrimLeft(t.buf, " \t\r\n")
			if t.buf == "" {
				return content, thinking
			}
			t.state = thinkPassthrough
		default:
			content += t.buf
			t.buf = ""
			return content, thinking
		}
	}
}

// flush releases anything held back at end of stream: an unterminated think
// block is still reasoning, and a held partial "<thi" was answer text.
func (t *thinkSplitter) flush() (content, thinking string) {
	switch t.state {
	case thinkInside:
		thinking = t.buf
	case thinkStart:
		content = t.buf
	}
	t.buf = ""
	return content, thinking
}

// reasoningField reads the reasoning text some servers return beside content
// (`reasoning` on Ollama/gpt-oss, `reasoning_content` on vLLM/llama.cpp/DeepSeek).
// The SDK has no typed field for either, so it is read from the raw object.
func reasoningField(rawObject string) string {
	if rawObject == "" {
		return ""
	}
	var fields struct {
		ReasoningContent string `json:"reasoning_content"`
		Reasoning        string `json:"reasoning"`
	}
	if json.Unmarshal([]byte(rawObject), &fields) != nil {
		return ""
	}
	if fields.ReasoningContent != "" {
		return fields.ReasoningContent
	}
	return fields.Reasoning
}

func usageFrom(u openai.CompletionUsage) llm.TokenUsage {
	return llm.TokenUsage{
		TokenCount:      u.PromptTokens,
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
	}
}

func (c *LocalClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(tools))

	attempts := 0
	for {
		attempts++
		completion, err := c.Client.Chat.Completions.New(ctx, params)
		if err != nil {
			retry, after, retryErr := c.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, c.wrapRequestError(retryErr)
			}
			if retry {
				logging.Warn(fmt.Sprintf("Retrying local request... attempt %d of %d", attempts, models.MaxRetries))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, c.wrapRequestError(err)
		}
		if len(completion.Choices) == 0 {
			return nil, fmt.Errorf("local model returned no choices")
		}

		choice := completion.Choices[0]
		var split thinkSplitter
		content, thinking := split.push(choice.Message.Content)
		tailContent, tailThinking := split.flush()
		content += tailContent
		thinking += tailThinking
		if reasoning := reasoningField(choice.Message.RawJSON()); reasoning != "" {
			thinking = reasoning
		}

		toolCalls := c.toolCalls(*completion)
		finishReason := c.finishReason(string(choice.FinishReason))
		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}
		return &llm.DriverResponse{
			Content:      content,
			Thinking:     strings.TrimSpace(thinking),
			ToolCalls:    toolCalls,
			Usage:        usageFrom(completion.Usage),
			FinishReason: finishReason,
		}, nil
	}
}

func (c *LocalClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}

	eventChan := make(chan llm.DriverEvent)
	send := func(ev llm.DriverEvent) bool {
		select {
		case eventChan <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	go func() {
		defer close(eventChan)
		attempts := 0
		for {
			attempts++
			stream := c.Client.Chat.Completions.NewStreaming(ctx, params)

			var (
				toolCalls    []message.ToolCall
				usage        llm.TokenUsage
				finishReason message.FinishReason
				finishSeen   bool
				content      strings.Builder
				thinking     strings.Builder
				split        thinkSplitter
			)
			emit := func(contentDelta, thinkingDelta string) bool {
				if thinkingDelta != "" {
					thinking.WriteString(thinkingDelta)
					if !send(llm.DriverEvent{Type: llm.EventThinkingDelta, Thinking: thinkingDelta}) {
						return false
					}
				}
				if contentDelta != "" {
					content.WriteString(contentDelta)
					if !send(llm.DriverEvent{Type: llm.EventContentDelta, Content: contentDelta}) {
						return false
					}
				}
				return true
			}

			for stream.Next() {
				chunk := stream.Current()

				// With include_usage the server sends usage in a LAST chunk with
				// choices: [] — after finish_reason — so completion is deferred
				// until the stream ends.
				if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
					usage = usageFrom(chunk.Usage)
				}
				if len(chunk.Choices) == 0 {
					continue
				}
				choice := chunk.Choices[0]

				if reasoning := reasoningField(choice.Delta.RawJSON()); reasoning != "" {
					if !emit("", reasoning) {
						return
					}
				}
				if choice.Delta.Content != "" {
					contentDelta, thinkingDelta := split.push(choice.Delta.Content)
					if !emit(contentDelta, thinkingDelta) {
						return
					}
				}

				for _, tc := range choice.Delta.ToolCalls {
					for len(toolCalls) <= int(tc.Index) {
						toolCalls = append(toolCalls, message.ToolCall{})
					}
					idx := int(tc.Index)
					if tc.ID != "" {
						toolCalls[idx].ID = tc.ID
						toolCalls[idx].Type = "function"
					}
					if tc.Function.Name != "" {
						toolCalls[idx].Name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						toolCalls[idx].Input += tc.Function.Arguments
					}
				}

				if choice.FinishReason != "" {
					finishSeen = true
					finishReason = c.finishReason(string(choice.FinishReason))
				}
			}

			if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
				retry, after, retryErr := c.shouldRetry(attempts, err)
				if retryErr == nil && retry && content.Len() == 0 && thinking.Len() == 0 && len(toolCalls) == 0 {
					logging.Warn(fmt.Sprintf("Retrying local stream... attempt %d of %d", attempts, models.MaxRetries))
					select {
					case <-ctx.Done():
						send(llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()})
						return
					case <-time.After(time.Duration(after) * time.Millisecond):
						continue
					}
				}
				if retryErr == nil {
					retryErr = err
				}
				send(llm.DriverEvent{Type: llm.EventError, Error: c.wrapRequestError(retryErr)})
				return
			}

			if !emit(split.flush()) {
				return
			}
			if !finishSeen {
				finishReason = message.FinishReasonUnknown
			}
			finished := toolCalls[:0:0]
			for _, tc := range toolCalls {
				if tc.ID == "" || tc.Name == "" {
					continue
				}
				tc.Finished = true
				finished = append(finished, tc)
			}
			if len(finished) > 0 {
				finishReason = message.FinishReasonToolUse
			}

			send(llm.DriverEvent{
				Type: llm.EventComplete,
				Response: &llm.DriverResponse{
					Content:      content.String(),
					Thinking:     strings.TrimSpace(thinking.String()),
					ToolCalls:    finished,
					Usage:        usage,
					FinishReason: finishReason,
				},
			})
			return
		}
	}()

	return eventChan
}

// shouldRetry retries only what a local server transiently does: 429/500/503.
// A transport failure (connection refused, daemon offline, relay error) is not
// transient from here — the old behavior of backing off through MaxRetries
// made a dead endpoint cost seconds before the user saw anything.
func (c *LocalClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}
	if apierr.StatusCode != 429 && apierr.StatusCode != 500 && apierr.StatusCode != 503 {
		return false, 0, err
	}
	if attempts > models.MaxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached: %d retries: %w", models.MaxRetries, err)
	}

	backoffMs := 2000 * (1 << (attempts - 1))
	retryMs := backoffMs + int(float64(backoffMs)*0.2)
	if apierr.Response != nil {
		if values := apierr.Response.Header.Values("Retry-After"); len(values) > 0 {
			var seconds int
			if _, scanErr := fmt.Sscanf(values[0], "%d", &seconds); scanErr == nil {
				retryMs = seconds * 1000
			}
		}
	}
	return true, int64(retryMs), nil
}

// wrapRequestError names the failing hop in terms a user can act on.
func (c *LocalClient) wrapRequestError(err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("local model server refused the connection (is it running?): %w", err)
	}
	return err
}
