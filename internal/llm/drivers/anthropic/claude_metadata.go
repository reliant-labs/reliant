// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// UserMetadata contains the formatted user metadata for API requests
type UserMetadata struct {
	UserID string `json:"user_id"`
}

// GetUserMetadata constructs user metadata from stored Claude OAuth account data.
// accountUUID is the account UUID from the OAuth token response.
// sessionID is an optional session identifier.
func GetUserMetadata(apiKey string, sessionID *string, accountUUID string) (*UserMetadata, error) {
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

	// Format the user ID as expected by Claude Code API
	formattedUserID := fmt.Sprintf("%s_account_%s_session_%s",
		accountUUID,
		accountUUID,
		sess)

	return &UserMetadata{
		UserID: formattedUserID,
	}, nil
}
