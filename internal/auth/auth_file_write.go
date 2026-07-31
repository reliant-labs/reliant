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

	// Atomic write: stage into a sibling temp file (0600) then rename over the
	// target. A concurrent reader (or a crash mid-write) never sees a truncated
	// or half-rotated auth file — it observes either the old or the new session.
	dir := filepath.Dir(authPath)
	tmp, err := os.CreateTemp(dir, ".reliant-auth-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp auth file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting auth file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing auth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("finalizing auth file: %w", err)
	}
	if err := os.Rename(tmpName, authPath); err != nil {
		return fmt.Errorf("replacing auth file: %w", err)
	}

	return nil
}
