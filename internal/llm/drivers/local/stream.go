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

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	"github.com/reliant-labs/reliant/internal/llm"
	openaidriver "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
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

	var completion *openai.ChatCompletion
	if err := c.retryer().Run(ctx, func(int) (ratelimit.Failure, bool) {
		var callErr error
		completion, callErr = c.Client.Chat.Completions.New(ctx, params)
		if callErr != nil {
			return localFailure(callErr), false
		}
		return ratelimit.Failure{}, true
	}, nil); err != nil {
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

		// The request retries through the shared ladder until the stream
		// yields its first chunk (an HTTP refusal surfaces from that first
		// Next).
		var stream *ssestream.Stream[openai.ChatCompletionChunk]
		hasNext := false
		emitCancelled := false
		openErr := c.retryer().Run(ctx, func(int) (ratelimit.Failure, bool) {
			stream = c.Client.Chat.Completions.NewStreaming(ctx, params)
			if hasNext = stream.Next(); hasNext {
				return ratelimit.Failure{}, true
			}
			return localFailure(stream.Err()), false
		}, func(wait llm.RetryWait) error {
			_ = stream.Close()
			if !send(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &wait}) {
				emitCancelled = true
				return ctx.Err()
			}
			return nil
		})
		if openErr != nil {
			if !emitCancelled {
				send(llm.DriverEvent{Type: llm.EventError, Error: c.wrapRequestError(openErr)})
			}
			return
		}

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

		for ok := hasNext; ok; ok = stream.Next() {
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

			// openai-go closes the stream itself only when Next reports
			// the end, so leaving mid-iteration must close it here.
			if reasoning := reasoningField(choice.Delta.RawJSON()); reasoning != "" {
				if !emit("", reasoning) {
					_ = stream.Close()
					return
				}
			}
			if choice.Delta.Content != "" {
				contentDelta, thinkingDelta := split.push(choice.Delta.Content)
				if !emit(contentDelta, thinkingDelta) {
					_ = stream.Close()
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

		// A failure once the stream is open is not retried here: its events
		// already reached the consumer. CallLLM's own retry owns it.
		if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
			send(llm.DriverEvent{Type: llm.EventError, Error: c.wrapRequestError(err)})
			return
		}

		if !emit(split.flush()) {
			return
		}
		if !finishSeen {
			finishReason = message.FinishReasonUnknown
		}
		// A call is a call once it names a tool. Its id is ours (see
		// llm.NewToolCallID): the server's may be missing, or repeated
		// within or across responses. Deltas were accumulated by index, so
		// nothing in this response needed the server's id.
		finished := toolCalls[:0:0]
		for _, tc := range toolCalls {
			if tc.Name == "" {
				continue
			}
			tc.ID = llm.NewToolCallID()
			tc.Type = "function"
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
	}()

	return eventChan
}

// retryer is the shared ladder. A local server answers 429 and 5xx when it is
// busy or loading a model, and those retry like any provider's.
func (c *LocalClient) retryer() ratelimit.Retryer {
	r := c.ladder
	if r.Provider == "" {
		r.Provider = "Local model"
		r.Log = "Local model request"
	}
	return r
}

// localFailure describes a failed request to the ladder. A transport failure
// (connection refused, daemon offline, relay error) is not transient from
// here: backing off through it made a dead endpoint cost seconds before the
// user saw anything.
func localFailure(err error) ratelimit.Failure {
	f := openaidriver.Failure(err)
	if f.Status == 0 {
		f.Fatal = true
	}
	return f
}

// wrapRequestError names the failing hop in terms a user can act on.
func (c *LocalClient) wrapRequestError(err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("local model server refused the connection (is it running?): %w", err)
	}
	return err
}
