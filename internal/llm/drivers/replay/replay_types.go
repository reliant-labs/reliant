// Copyright (c) 2025 Reliant Labs
package replay

import (
	"encoding/json"
	"time"
)

// ComprehensiveReplayData represents a complete conversation tree with all sessions
type ComprehensiveReplayData struct {
	RootSessionID string                    `json:"root_session_id"`
	Title         string                    `json:"title"`
	Sessions      map[string]*ReplaySession `json:"sessions"`      // Map of session ID to session data
	MessageOrder  []string                  `json:"message_order"` // Global order of message IDs across all sessions
	CreatedAt     time.Time                 `json:"created_at"`
	ExtractedAt   time.Time                 `json:"extracted_at"`
}

// ReplaySession represents a single session in the replay
type ReplaySession struct {
	ID              string                 `json:"id"`
	ParentSessionID string                 `json:"parent_session_id,omitempty"`
	Title           string                 `json:"title"`
	Agent           string                 `json:"agent,omitempty"`
	AgentState      string                 `json:"agent_state,omitempty"`
	Messages        []ComprehensiveMessage `json:"messages"`
	ChildSessions   []string               `json:"child_sessions,omitempty"` // IDs of child sessions
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	MessageCount    int                    `json:"message_count"`
	IsCompaction    bool                   `json:"is_compaction,omitempty"`
	CompactedFrom   string                 `json:"compacted_from,omitempty"` // Original session ID if this is a compaction
}

// ComprehensiveMessage represents a message with full metadata
type ComprehensiveMessage struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Agent     string          `json:"agent,omitempty"`
	Model     string          `json:"model,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	// For tracking purposes during replay
	ParentMessageID string `json:"parent_message_id,omitempty"`
	IsCompacted     bool   `json:"is_compacted,omitempty"`
}

// ReplayState tracks the current state during replay
type ReplayState struct {
	CurrentSessionID  string
	CurrentMessageIdx int
	SessionMessageIdx map[string]int // Track position in each session
	ProcessedMessages map[string]bool
	ActiveAgent       string
}

// FindNextMessage finds the next message to replay based on the global order
func (r *ComprehensiveReplayData) FindNextMessage(state *ReplayState) (*ComprehensiveMessage, *ReplaySession, error) {
	// Use the global message order to determine next message
	for _, messageID := range r.MessageOrder {
		if !state.ProcessedMessages[messageID] {
			// Find which session contains this message
			for sessionID, session := range r.Sessions {
				for _, msg := range session.Messages {
					if msg.ID == messageID {
						state.ProcessedMessages[messageID] = true
						state.CurrentSessionID = sessionID
						return &msg, session, nil
					}
				}
			}
		}
	}
	return nil, nil, nil // No more messages
}

// GetSessionTree returns all sessions in a tree structure starting from root
func (r *ComprehensiveReplayData) GetSessionTree() map[string][]string {
	tree := make(map[string][]string)
	for _, session := range r.Sessions {
		if session.ParentSessionID != "" {
			tree[session.ParentSessionID] = append(tree[session.ParentSessionID], session.ID)
		}
	}
	return tree
}

// GetMessagesForSession returns all messages for a specific session
func (r *ComprehensiveReplayData) GetMessagesForSession(sessionID string) []ComprehensiveMessage {
	if session, ok := r.Sessions[sessionID]; ok {
		return session.Messages
	}
	return nil
}

// GetAllMessagesInOrder returns all messages across all sessions in chronological order
func (r *ComprehensiveReplayData) GetAllMessagesInOrder() []ComprehensiveMessage {
	var messages []ComprehensiveMessage
	for _, messageID := range r.MessageOrder {
		for _, session := range r.Sessions {
			for _, msg := range session.Messages {
				if msg.ID == messageID {
					messages = append(messages, msg)
					break
				}
			}
		}
	}
	return messages
}
