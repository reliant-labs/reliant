// Copyright (c) 2025 Reliant Labs

package drivers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// MintReliantUserAPIKey requests a new LiteLLM virtual key for the given user
// from the configured Reliant/LiteLLM admin API. The returned `sk-...` key
// carries the user's identity, model access, and budget inside the proxy, so
// downstream LLM calls authenticated with it are automatically attributed to
// userID — no JWT required.
//
// Returns an error if the admin credential isn't configured or the upstream
// call fails. Caller is responsible for persisting the returned key.
func MintReliantUserAPIKey(ctx context.Context, userID string) (string, error) {
	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" {
		return "", fmt.Errorf("reliant admin: user_id is required")
	}

	adminKey := strings.TrimSpace(os.Getenv("RELIANT_ADMIN_API_KEY"))
	if adminKey == "" {
		adminKey = strings.TrimSpace(os.Getenv("LITELLM_MASTER_KEY"))
	}
	if adminKey == "" {
		err := fmt.Errorf("reliant admin credential not configured: set RELIANT_ADMIN_API_KEY")
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", err)
		return "", err
	}

	baseURL := strings.TrimRight(ResolveReliantBaseURL(""), "/")
	endpoint := baseURL + "/key/generate"

	payload := map[string]string{
		"user_id":   trimmedUserID,
		"key_alias": "user-" + trimmedUserID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		wrapped := fmt.Errorf("reliant admin: marshal request: %w", err)
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", wrapped)
		return "", wrapped
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		wrapped := fmt.Errorf("reliant admin: build request: %w", err)
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", wrapped)
		return "", wrapped
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("reliant admin: http call: %w", err)
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", wrapped)
		return "", wrapped
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	truncated := string(respBody)
	if len(truncated) > 512 {
		truncated = truncated[:512]
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("reliant admin: unexpected status %d: %s", resp.StatusCode, truncated)
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", wrapped)
		return "", wrapped
	}

	var parsed struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		wrapped := fmt.Errorf("reliant admin: decode response: %w: %s", err, truncated)
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", wrapped)
		return "", wrapped
	}
	if strings.TrimSpace(parsed.Key) == "" {
		wrapped := fmt.Errorf("reliant admin: empty key in response: %s", truncated)
		logging.Error("Failed to mint Reliant virtual key", "user_id", trimmedUserID, "error", wrapped)
		return "", wrapped
	}

	logging.Info("Minted Reliant virtual key", "user_id", trimmedUserID)
	return parsed.Key, nil
}

// RotateReliantUserAPIKey deletes the user's existing LiteLLM virtual key
// (best-effort) and mints a fresh one. Returns the new sk-... key.
//
// Use this when a key is suspected compromised, or as part of routine rotation.
// The new key carries the same userID attribution as the old one; budget and
// model-access state on the LiteLLM side is reset to defaults unless the caller
// re-applies plan settings afterward.
func RotateReliantUserAPIKey(ctx context.Context, userID, oldKey string) (string, error) {
	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" {
		return "", fmt.Errorf("reliant admin: user_id is required")
	}

	trimmedOldKey := strings.TrimSpace(oldKey)
	if trimmedOldKey != "" {
		// Best-effort: a stale key is better than a failed rotation that leaves
		// the user with no key at all.
		if err := deleteReliantUserAPIKey(ctx, trimmedUserID, trimmedOldKey); err != nil {
			logging.Warn("Failed to delete old Reliant virtual key during rotation; proceeding to mint",
				"user_id", trimmedUserID, "error", err)
		}
	}

	return MintReliantUserAPIKey(ctx, trimmedUserID)
}

func deleteReliantUserAPIKey(ctx context.Context, userID, oldKey string) error {
	adminKey, err := reliantAdminCredential()
	if err != nil {
		return err
	}

	baseURL := strings.TrimRight(ResolveReliantBaseURL(""), "/")
	endpoint := baseURL + "/key/delete"

	payload := map[string]any{"keys": []string{oldKey}}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("reliant admin: marshal delete request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("reliant admin: build delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := reliantAdminClient().Do(req)
	if err != nil {
		return fmt.Errorf("reliant admin: delete http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		truncated := string(respBody)
		if len(truncated) > 512 {
			truncated = truncated[:512]
		}
		return fmt.Errorf("reliant admin: delete unexpected status %d: %s", resp.StatusCode, truncated)
	}

	logging.Info("Deleted Reliant virtual key", "user_id", userID)
	return nil
}

func reliantAdminCredential() (string, error) {
	adminKey := strings.TrimSpace(os.Getenv("RELIANT_ADMIN_API_KEY"))
	if adminKey == "" {
		adminKey = strings.TrimSpace(os.Getenv("LITELLM_MASTER_KEY"))
	}
	if adminKey == "" {
		return "", fmt.Errorf("reliant admin credential not configured: set RELIANT_ADMIN_API_KEY")
	}
	return adminKey, nil
}

func reliantAdminClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
