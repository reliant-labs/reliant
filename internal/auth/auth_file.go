package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const authFileName = "reliant-auth.json"

type persistedAuthSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

// CurrentAuthFilePath returns the OS-native location of the persisted auth session.
func CurrentAuthFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	var authDir string
	switch runtime.GOOS {
	case "darwin":
		authDir = filepath.Join(homeDir, "Library", "Application Support", "reliant", "auth")
	case "windows":
		authDir = filepath.Join(homeDir, "AppData", "Roaming", "reliant", "auth")
	default:
		authDir = filepath.Join(homeDir, ".config", "reliant", "auth")
	}

	return filepath.Join(authDir, authFileName), nil
}

// CandidateAuthFilePaths returns all known auth file locations used across supported OSes.
func CandidateAuthFilePaths() ([]string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	return []string{
		filepath.Join(homeDir, "Library", "Application Support", "reliant", "auth", authFileName),
		filepath.Join(homeDir, ".config", "reliant", "auth", authFileName),
		filepath.Join(homeDir, "AppData", "Roaming", "reliant", "auth", authFileName),
	}, nil
}

func readPersistedAuthSession() (*persistedAuthSession, string, error) {
	authPaths, err := CandidateAuthFilePaths()
	if err != nil {
		return nil, "", err
	}

	for _, authPath := range authPaths {
		data, readErr := os.ReadFile(authPath)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, "", fmt.Errorf("failed to read auth file %s: %w", authPath, readErr)
		}

		var authSession persistedAuthSession
		if err := json.Unmarshal(data, &authSession); err != nil {
			return nil, "", fmt.Errorf("failed to parse auth file %s: %w", authPath, err)
		}

		return &authSession, authPath, nil
	}

	return nil, "", nil
}

// ReadAccessTokenFromAuthFile returns a VALID Supabase access token, refreshing
// it transparently when the stored token is expired or within the staleness
// skew (see EnsureFreshAccessToken). This is the seam every JWT-authenticated
// CLI path composes over: resolveConnection and the other legacy-JWT call sites
// call this and get a fresh token without knowing a refresh happened.
//
// Returns empty string (no error) when no auth file exists — an unauthenticated
// machine, not a failure, so callers can keep their existing "" checks. Returns
// ErrSessionExpired when the token is stale and cannot be refreshed (no
// refresh_token, or the refresh endpoint rejected it); callers surface that as
// "session expired — run `reliant auth login`". It never returns a stale token.
//
// For a pure, non-mutating read that must not touch the network (diagnostics
// such as `reliant auth status`), use PeekAccessTokenFromAuthFile instead.
func ReadAccessTokenFromAuthFile() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshRequestTimeout)
	defer cancel()
	return EnsureFreshAccessToken(ctx)
}

// PeekAccessTokenFromAuthFile reads access_token from the persisted auth session
// WITHOUT attempting a refresh. Use it for diagnostics that must report the
// on-disk token exactly as stored and never mutate the file or touch the
// network. Returns empty string when no auth file exists in known locations.
func PeekAccessTokenFromAuthFile() (string, error) {
	authSession, _, err := readPersistedAuthSession()
	if err != nil {
		return "", err
	}
	if authSession == nil {
		return "", nil
	}
	return strings.TrimSpace(authSession.AccessToken), nil
}

// ReadUserIDFromAuthFile reads user.id from the persisted auth session.
// Returns empty string when no auth file exists in known locations.
func ReadUserIDFromAuthFile() (string, error) {
	authSession, _, err := readPersistedAuthSession()
	if err != nil {
		return "", err
	}
	if authSession == nil {
		return "", nil
	}
	return strings.TrimSpace(authSession.User.ID), nil
}
