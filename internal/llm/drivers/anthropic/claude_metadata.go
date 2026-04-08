// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// UserMetadata contains the formatted user metadata for API requests
type UserMetadata struct {
	UserID string `json:"user_id"`
}

// userIDPayload is the JSON structure sent in metadata.user_id to the Claude API.
type userIDPayload struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

// GetUserMetadata constructs user metadata from stored Claude OAuth account data.
// accountUUID is the account UUID from the OAuth token response.
// sessionID is an optional session identifier.
// reliantUserID is the Supabase user ID used to derive the device ID.
func GetUserMetadata(apiKey string, sessionID *string, accountUUID, reliantUserID string) (*UserMetadata, error) {
	// Only process if this is an sk-ant-oat key
	if !strings.HasPrefix(apiKey, "sk-ant-oat") {
		return nil, nil
	}

	if accountUUID == "" {
		return nil, fmt.Errorf("account UUID is required for Claude Code metadata")
	}

	var sess string
	if sessionID != nil {
		sess = *sessionID
	} else {
		sess = uuid.New().String()
	}

	// Derive a stable device ID: SHA256(reliantUserID + accountUUID)
	h := sha256.Sum256([]byte(reliantUserID + accountUUID))
	deviceID := fmt.Sprintf("%x", h)

	payload := userIDPayload{
		DeviceID:    deviceID,
		AccountUUID: accountUUID,
		SessionID:   sess,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal user ID payload: %w", err)
	}

	return &UserMetadata{
		UserID: string(encoded),
	}, nil
}
