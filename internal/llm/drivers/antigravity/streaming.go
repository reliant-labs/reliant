// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// maxSSELineBytes bounds one buffered SSE frame. Thought signatures run to
// several KB and a frame carries one per part, so the default 64KB scanner
// buffer is not enough headroom.
const maxSSELineBytes = 4 * 1024 * 1024

// StreamResponse POSTs the double-wrapped request and translates the
// double-wrapped SSE frames into driver events.
func (c *Client) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, toolList []toolsPkg.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		body, err := json.Marshal(c.buildEnvelope(messages, prompts, toolList))
		if err != nil {
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("antigravity: failed to marshal request: %w", err)}
			return
		}

		logging.Debug("[ANTIGRAVITY] Streaming request",
			"model", c.requestModelID(),
			"messageCount", len(messages),
			"toolCount", len(toolList))

		var resp *http.Response
		for attempt := 1; ; attempt++ {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
			if reqErr != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("antigravity: failed to create request: %w", reqErr)}
				return
			}
			req.Header.Set("Accept", "text/event-stream")

			resp, err = c.httpClient.Do(req)
			if err != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("antigravity: request failed: %w", err)}
				return
			}
			if resp.StatusCode == http.StatusOK {
				break
			}

			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			status := resp.StatusCode
			_ = resp.Body.Close()

			if !isRetryableStatus(status) || attempt > models.MaxRetries {
				eventChan <- llm.DriverEvent{
					Type:  llm.EventError,
					Error: fmt.Errorf("antigravity: API request failed with status %d: %s", status, string(errBody)),
				}
				return
			}

			delay := retryDelay(attempt, resp)
			logging.Warn("[ANTIGRAVITY] Retrying stream request",
				"status", status, "attempt", attempt, "maxRetries", models.MaxRetries, "delay", delay)
			// Emitted BEFORE the sleep so a caller can record that this run is
			// parked rather than working.
			eventChan <- llm.DriverEvent{
				Type: llm.EventRetryWait,
				Retry: &llm.RetryWait{
					Attempt:     attempt,
					MaxAttempts: models.MaxRetries,
					Delay:       delay,
					StatusCode:  status,
					Reason:      fmt.Sprintf("http_%d", status),
				},
			}
			select {
			case <-ctx.Done():
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
				return
			case <-time.After(delay):
			}
		}
		defer resp.Body.Close()

		// Closing the body is what unblocks the scanner on cancellation.
		go func() {
			<-ctx.Done()
			resp.Body.Close()
		}()

		c.consumeStream(ctx, resp.Body, eventChan)
	}()

	return eventChan
}

// consumeStream reads SSE frames, unwraps each one, and emits driver events.
func (c *Client) consumeStream(ctx context.Context, body io.Reader, eventChan chan<- llm.DriverEvent) {
	var (
		text              strings.Builder
		thinking          strings.Builder
		thinkingSignature string
		toolCalls         []message.ToolCall
		contentStarted    bool
		thinkingStarted   bool
		finalResp         *generateResp
		lastFinishReason  string
	)

	err := scanFrames(body, func(raw []byte) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var frame streamFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			logging.Debug("[ANTIGRAVITY] Skipping unparseable SSE frame", "error", err)
			return nil
		}
		// A frame with no "response" member is not a Gemini response at the
		// top level — it is a keepalive or a shape we do not model. Skipping is
		// correct; decoding it AS a response is the SDK bug this driver exists
		// to avoid.
		if frame.Response == nil {
			return nil
		}
		finalResp = frame.Response

		for _, cand := range frame.Response.Candidates {
			if cand == nil {
				continue
			}
			if cand.FinishReason != "" {
				lastFinishReason = cand.FinishReason
			}
			if cand.Content == nil {
				continue
			}
			// Signatures do not have to ride on the functionCall part they
			// belong to. They arrive on the preceding thought part, or on a
			// bare signature-only part beside the call, and a call replayed
			// without one makes the NEXT request fail with "Function call is
			// missing a thought_signature" — naming a position several turns
			// back, which is why the fault reads as random.
			//
			// So the candidate's signature is collected and applied to every
			// call in that candidate that did not carry its own. Candidate
			// scope is the correct scope: it is the unit the server signs.
			candidateSig := ""
			var unsignedCalls []int
			for _, p := range cand.Content.Parts {
				if p == nil {
					continue
				}
				if p.ThoughtSignature != "" && candidateSig == "" {
					candidateSig = p.ThoughtSignature
				}
				switch {
				case p.FunctionCall != nil:
					args, _ := json.Marshal(p.FunctionCall.Args)
					call := message.ToolCall{
						ID: "call_" + uuid.New().String(),
						// Stripped on the way back in, the mirror of the
						// prefix convertTools applied on the way out.
						Name:             unprefixedToolName(p.FunctionCall.Name),
						Input:            string(args),
						Type:             "function",
						Finished:         true,
						ThoughtSignature: p.ThoughtSignature,
					}
					if call.ThoughtSignature == "" {
						unsignedCalls = append(unsignedCalls, len(toolCalls))
					}
					toolCalls = append(toolCalls, call)
					eventChan <- llm.DriverEvent{Type: llm.EventToolUseStart, ToolCall: &call}
					eventChan <- llm.DriverEvent{Type: llm.EventToolUseStop, ToolCall: &call}

				case p.Thought:
					if p.ThoughtSignature != "" {
						thinkingSignature = p.ThoughtSignature
					}
					if p.Text == "" {
						continue
					}
					if !thinkingStarted {
						eventChan <- llm.DriverEvent{Type: llm.EventThinkingStart}
						thinkingStarted = true
					}
					thinking.WriteString(p.Text)
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingDelta, Thinking: p.Text}

				default:
					// A signature can ride on a plain text part with no text
					// at all (the capture's final frame does exactly that), so
					// capture it before the empty-text check.
					if p.ThoughtSignature != "" && thinkingSignature == "" {
						thinkingSignature = p.ThoughtSignature
					}
					if p.Text == "" {
						continue
					}
					if !contentStarted {
						eventChan <- llm.DriverEvent{Type: llm.EventContentStart}
						contentStarted = true
					}
					text.WriteString(p.Text)
					eventChan <- llm.DriverEvent{Type: llm.EventContentDelta, Content: p.Text}
				}
			}

			// Backfill AFTER the loop: a signature-only part can follow the
			// call it belongs to, so the candidate's signature is not known
			// until every part has been read.
			if candidateSig != "" {
				for _, idx := range unsignedCalls {
					toolCalls[idx].ThoughtSignature = candidateSig
				}
			} else if len(unsignedCalls) > 0 {
				// Worth a line in the log: this is the state that will 400 on
				// the next request rather than on this one.
				logging.Warn("[ANTIGRAVITY] Function call arrived with no thought signature anywhere in its candidate",
					"unsignedCalls", len(unsignedCalls))
			}
		}
		return nil
	})

	if err != nil && ctx.Err() == nil {
		eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("antigravity: error reading stream: %w", err)}
		return
	}
	if ctx.Err() != nil {
		eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
		return
	}

	if thinkingStarted {
		eventChan <- llm.DriverEvent{Type: llm.EventThinkingStop}
	}
	if contentStarted {
		eventChan <- llm.DriverEvent{Type: llm.EventContentStop}
	}

	reason := finishReason(lastFinishReason)
	if len(toolCalls) > 0 {
		reason = message.FinishReasonToolUse
	}

	eventChan <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Content:           text.String(),
			Thinking:          thinking.String(),
			ThinkingSignature: thinkingSignature,
			ToolCalls:         toolCalls,
			Usage:             usage(finalResp),
			FinishReason:      reason,
		},
	}
}

// scanFrames splits an SSE body into frame payloads and hands each to onFrame.
//
// It accepts both wire shapes we have seen: the single-line `data: {…}` form,
// and the pretty-printed form in the capture, where `data:` is alone on its
// line and the JSON object follows across several unprefixed lines until a
// blank line ends the frame.
func scanFrames(body io.Reader, onFrame func(raw []byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	var buf strings.Builder
	open := false

	flush := func() error {
		if !open {
			return nil
		}
		payload := strings.TrimSpace(buf.String())
		buf.Reset()
		open = false
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		return onFrame([]byte(payload))
	}

	for scanner.Scan() {
		line := scanner.Text()

		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}

		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			// A new data field ends any frame still open.
			if err := flush(); err != nil {
				return err
			}
			open = true
			buf.WriteString(strings.TrimPrefix(rest, " "))
			continue
		}

		// Other SSE fields (event:, id:, retry:, comments) carry nothing we
		// model. Anything else is a continuation of the open frame's payload.
		if !open || isSSEField(line) {
			continue
		}
		buf.WriteString(line)
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

// isSSEField reports whether a line is a non-data SSE field or comment, which
// must not be mistaken for a continuation line of a pretty-printed payload.
func isSSEField(line string) bool {
	for _, prefix := range []string{":", "event:", "id:", "retry:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
