// Copyright (c) 2025 Reliant Labs
package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// ComprehensiveReplayDriver replays entire conversation trees with all sessions
type ComprehensiveReplayDriver struct {
	mu           sync.RWMutex
	data         *ComprehensiveReplayData
	state        *ReplayState
	currentAgent string
	model        models.Model
}

// Name returns the name of the driver
func (d *ComprehensiveReplayDriver) Name() string {
	return "replay"
}

// NewComprehensiveReplayDriver creates a new comprehensive replay driver from a file
func NewComprehensiveReplayDriver(filePath string) (*ComprehensiveReplayDriver, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read replay file: %w", err)
	}

	var replayData ComprehensiveReplayData
	if err := json.Unmarshal(data, &replayData); err != nil {
		return nil, fmt.Errorf("failed to parse replay file: %w", err)
	}

	state := &ReplayState{
		CurrentSessionID:  replayData.RootSessionID,
		SessionMessageIdx: make(map[string]int),
		ProcessedMessages: make(map[string]bool),
	}

	logging.Info("Loaded comprehensive replay data",
		"root_session", replayData.RootSessionID,
		"total_sessions", len(replayData.Sessions),
		"total_messages", len(replayData.MessageOrder))

	return &ComprehensiveReplayDriver{
		data:  &replayData,
		state: state,
		model: models.Model{
			Name: "comprehensive-replay",
			ID:   "replay",
		},
	}, nil
}

// Model returns the model configuration
func (d *ComprehensiveReplayDriver) Model() models.Model {
	return d.model
}

func (d *ComprehensiveReplayDriver) ValidateKey(ctx context.Context) error {
	// Replay driver always validates successfully
	return nil
}

// SendMessages returns the next appropriate message from the replay
func (d *ComprehensiveReplayDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Find the next assistant message to replay
	nextMsg, session, err := d.findNextAssistantMessage()
	if err != nil {
		return nil, err
	}
	if nextMsg == nil {
		return nil, fmt.Errorf("no more assistant messages in replay")
	}

	// Update current agent if this session has a different agent
	if session.Agent != "" && session.Agent != d.currentAgent {
		d.currentAgent = session.Agent
		logging.Debug("Switching to agent", "agent", session.Agent, "session", session.ID)
	}

	// Extract content and tool calls from the message
	content, toolCalls, finishReason, err := d.extractMessageParts(nextMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to extract message parts: %w", err)
	}

	logging.Debug("Replaying message",
		"session", nextMsg.SessionID,
		"agent", nextMsg.Agent,
		"role", nextMsg.Role,
		"content_preview", truncateString(content, 100),
		"tool_calls", len(toolCalls))

	return &llm.DriverResponse{
		Content:      content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: llm.TokenUsage{
			TokenCount: 100, // Mock value for context size
		},
	}, nil
}

// StreamResponse streams the next message from the replay
func (d *ComprehensiveReplayDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)

	go func() {
		defer close(ch)

		resp, err := d.SendMessages(ctx, prompts, messages, tools)
		if err != nil {
			ch <- llm.DriverEvent{
				Type:  llm.EventError,
				Error: err,
			}
			return
		}

		// Send content start event if there's content
		if resp.Content != "" {
			ch <- llm.DriverEvent{
				Type: llm.EventContentStart,
			}

			// Stream the content character by character
			for _, char := range resp.Content {
				select {
				case <-ctx.Done():
					ch <- llm.DriverEvent{
						Type:  llm.EventError,
						Error: ctx.Err(),
					}
					return
				case ch <- llm.DriverEvent{
					Type:    llm.EventContentDelta,
					Content: string(char),
				}:
				}
			}

			// Send content stop event
			ch <- llm.DriverEvent{
				Type: llm.EventContentStop,
			}
		}

		// Stream tool calls if present
		for _, toolCall := range resp.ToolCalls {
			// Make a copy for the closure
			tc := toolCall
			// Send tool use start event
			ch <- llm.DriverEvent{
				Type:     llm.EventToolUseStart,
				ToolCall: &tc,
			}

			// Could stream tool call input character by character here if desired
			// For now, just send the complete tool call
			ch <- llm.DriverEvent{
				Type:     llm.EventToolUseDelta,
				ToolCall: &tc,
			}

			// Send tool use stop event
			ch <- llm.DriverEvent{
				Type:     llm.EventToolUseStop,
				ToolCall: &tc,
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

// findNextAssistantMessage finds the next assistant message in the replay order
func (d *ComprehensiveReplayDriver) findNextAssistantMessage() (*ComprehensiveMessage, *ReplaySession, error) {
	for _, messageID := range d.data.MessageOrder {
		if d.state.ProcessedMessages[messageID] {
			continue
		}

		// Find the message and its session
		for sessionID, session := range d.data.Sessions {
			for i, msg := range session.Messages {
				if msg.ID == messageID {
					// Mark as processed
					d.state.ProcessedMessages[messageID] = true

					// Skip non-assistant messages but mark them as processed
					if msg.Role != "assistant" {
						continue
					}

					// Update session index
					d.state.SessionMessageIdx[sessionID] = i
					d.state.CurrentSessionID = sessionID

					return &msg, session, nil
				}
			}
		}
	}

	return nil, nil, nil
}

// extractMessageParts extracts the text content, tool calls, and finish reason from a replay message
func (d *ComprehensiveReplayDriver) extractMessageParts(msg *ComprehensiveMessage) (string, []message.ToolCall, message.FinishReason, error) {
	// The content is stored as an array of wrapped parts in the database
	var parts []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}

	if err := json.Unmarshal(msg.Content, &parts); err != nil {
		// Fallback: try as plain string
		var content string
		if err := json.Unmarshal(msg.Content, &content); err == nil {
			return content, nil, message.FinishReasonEndTurn, nil
		}
		return "", nil, message.FinishReasonEndTurn, fmt.Errorf("failed to parse message content: %w", err)
	}

	var textContent string
	var toolCalls []message.ToolCall
	finishReason := message.FinishReasonEndTurn // default

	for _, part := range parts {
		switch part.Type {
		case "text":
			var textData struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(part.Data, &textData); err == nil {
				textContent += textData.Text
			}
		case "reasoning":
			// Skip reasoning content - it's not part of the visible output
			continue
		case "tool_call":
			var toolCall message.ToolCall
			if err := json.Unmarshal(part.Data, &toolCall); err == nil {
				// Fix field name mappings if needed
				var toolCallMap map[string]interface{}
				if err := json.Unmarshal(part.Data, &toolCallMap); err == nil {
					// Handle different field name conventions
					if id, ok := toolCallMap["ID"].(string); ok {
						toolCall.ID = id
					} else if id, ok := toolCallMap["id"].(string); ok {
						toolCall.ID = id
					}

					if name, ok := toolCallMap["Name"].(string); ok {
						toolCall.Name = name
					} else if name, ok := toolCallMap["name"].(string); ok {
						toolCall.Name = name
					}

					// Input can be string or object
					if input, ok := toolCallMap["Input"]; ok {
						if inputStr, ok := input.(string); ok {
							toolCall.Input = inputStr
						} else {
							inputBytes, _ := json.Marshal(input)
							toolCall.Input = string(inputBytes)
						}
					} else if input, ok := toolCallMap["input"]; ok {
						if inputStr, ok := input.(string); ok {
							toolCall.Input = inputStr
						} else {
							inputBytes, _ := json.Marshal(input)
							toolCall.Input = string(inputBytes)
						}
					}

					if toolType, ok := toolCallMap["Type"].(string); ok {
						toolCall.Type = toolType
					} else if toolType, ok := toolCallMap["type"].(string); ok {
						toolCall.Type = toolType
					} else {
						toolCall.Type = "function" // default
					}

					if finished, ok := toolCallMap["Finished"].(bool); ok {
						toolCall.Finished = finished
					} else if finished, ok := toolCallMap["finished"].(bool); ok {
						toolCall.Finished = finished
					} else {
						toolCall.Finished = true // default
					}
				}
				toolCalls = append(toolCalls, toolCall)
			}
		case "tool_result":
			// Tool results are not part of the driver response
			continue
		case "finish":
			var finishData struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(part.Data, &finishData); err == nil {
				switch finishData.Reason {
				case "tool_use":
					finishReason = message.FinishReasonToolUse
				case "stop", "end_turn":
					finishReason = message.FinishReasonEndTurn
				case "max_tokens":
					finishReason = message.FinishReasonMaxTokens
				default:
					finishReason = message.FinishReasonEndTurn
				}
			}
		}
	}

	// If we have tool calls, ensure finish reason is tool_use
	if len(toolCalls) > 0 && finishReason == message.FinishReasonEndTurn {
		finishReason = message.FinishReasonToolUse
	}

	return textContent, toolCalls, finishReason, nil
}

// truncateString truncates a string to maxLen and adds "..." suffix
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Reset resets the replay to the beginning
func (d *ComprehensiveReplayDriver) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state = &ReplayState{
		CurrentSessionID:  d.data.RootSessionID,
		SessionMessageIdx: make(map[string]int),
		ProcessedMessages: make(map[string]bool),
	}
	d.currentAgent = ""
}

// GetCurrentSession returns the current session being replayed
func (d *ComprehensiveReplayDriver) GetCurrentSession() (*ReplaySession, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if session, ok := d.data.Sessions[d.state.CurrentSessionID]; ok {
		return session, nil
	}
	return nil, fmt.Errorf("current session not found: %s", d.state.CurrentSessionID)
}

// GetProgress returns replay progress information
func (d *ComprehensiveReplayDriver) GetProgress() (processed, total int) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return len(d.state.ProcessedMessages), len(d.data.MessageOrder)
}

// GetSessionTree returns the session hierarchy
func (d *ComprehensiveReplayDriver) GetSessionTree() map[string][]string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.data.GetSessionTree()
}
