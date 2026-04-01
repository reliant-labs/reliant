// Copyright (c) 2025 Reliant Labs
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeAuthSession is the on-disk format for the auth file.
// It must stay compatible with the existing persistedAuthSession read path.
type writeAuthSession struct {
	AccessToken  string        `json:"access_token"`
	RefreshToken string        `json:"refresh_token"`
	User         writeAuthUser `json:"user"`
}

type writeAuthUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// WriteAuthSession persists the auth session to the platform-specific auth file.
func WriteAuthSession(accessToken, refreshToken, userID, email string) error {
	authPath, err := CurrentAuthFilePath()
	if err != nil {
		return fmt.Errorf("determining auth file path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		return fmt.Errorf("creating auth directory: %w", err)
	}

	session := writeAuthSession{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		User: writeAuthUser{
			ID:    userID,
			Email: email,
		},
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling auth session: %w", err)
	}

	if err := os.WriteFile(authPath, data, 0600); err != nil {
		return fmt.Errorf("writing auth file: %w", err)
	}

	return nil
}
