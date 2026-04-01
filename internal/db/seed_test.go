// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
)

func TestSeedAPIKeyFromEnv(t *testing.T) {
	// Create in-memory database
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer db.Close()

	// Run migrations
	if err := RunMigrations(db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create a temp home directory with auth file
	tempHome := t.TempDir()

	// Override HOME for the test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", oldHome)

	authFilePath, err := auth.CurrentAuthFilePath()
	if err != nil {
		t.Fatalf("failed to compute auth file path: %v", err)
	}
	authDir := filepath.Dir(authFilePath)
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	// Write reliant-auth.json with test user ID
	authData := map[string]interface{}{
		"user": map[string]string{
			"id": "test-user-123",
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(authFilePath, authJSON, 0644); err != nil {
		t.Fatalf("failed to write reliant-auth.json: %v", err)
	}

	// Test 1: No env vars set - should skip silently
	err = seedAPIKeyFromEnv(db)
	if err != nil {
		t.Errorf("expected no error when env vars not set, got: %v", err)
	}

	// Test 2: Only API key set - should skip silently
	os.Setenv("RELIANT_SEED_API_KEY", "test-key")
	defer os.Unsetenv("RELIANT_SEED_API_KEY")

	err = seedAPIKeyFromEnv(db)
	if err != nil {
		t.Errorf("expected no error with partial env vars, got: %v", err)
	}

	// Test 3: Both env vars set - should create setting using user ID from auth file
	os.Setenv("RELIANT_SEED_PROVIDER", "anthropic")
	defer os.Unsetenv("RELIANT_SEED_PROVIDER")

	err = seedAPIKeyFromEnv(db)
	if err != nil {
		t.Fatalf("expected no error when creating setting, got: %v", err)
	}

	// Verify API key was created in dedicated table
	var apiKey string
	err = db.QueryRow(
		"SELECT api_key FROM api_keys WHERE user_id = ? AND provider = ?",
		"test-user-123", "anthropic",
	).Scan(&apiKey)
	if err != nil {
		t.Fatalf("failed to query created API key: %v", err)
	}
	if apiKey != "test-key" {
		t.Errorf("expected api_key 'test-key', got '%s'", apiKey)
	}

	// Test 4: Run again with different key - should update existing
	os.Setenv("RELIANT_SEED_API_KEY", "new-test-key")

	err = seedAPIKeyFromEnv(db)
	if err != nil {
		t.Fatalf("expected no error when updating API key, got: %v", err)
	}

	// Verify API key was updated
	err = db.QueryRow(
		"SELECT api_key FROM api_keys WHERE user_id = ? AND provider = ?",
		"test-user-123", "anthropic",
	).Scan(&apiKey)
	if err != nil {
		t.Fatalf("failed to query updated API key: %v", err)
	}
	if apiKey != "new-test-key" {
		t.Errorf("expected api_key 'new-test-key', got '%s'", apiKey)
	}

	// Test 5: Invalid provider - should return error
	os.Setenv("RELIANT_SEED_PROVIDER", "invalid-provider")

	err = seedAPIKeyFromEnv(db)
	if err == nil {
		t.Error("expected error for invalid provider, got nil")
	}
}

func TestGetUserIDFromAuthFile(t *testing.T) {
	// Create a temp home directory
	tempHome := t.TempDir()

	// Override HOME for the test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", oldHome)

	// Test 1: No auth file - should return empty string
	userID, err := getUserIDFromAuthFile()
	if err != nil {
		t.Errorf("expected no error when auth file doesn't exist, got: %v", err)
	}
	if userID != "" {
		t.Errorf("expected empty user ID when auth file doesn't exist, got: %s", userID)
	}

	// Test 2: Create auth file with user id
	authFilePath, err := auth.CurrentAuthFilePath()
	if err != nil {
		t.Fatalf("failed to compute auth file path: %v", err)
	}
	authDir := filepath.Dir(authFilePath)
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	authData := map[string]interface{}{
		"user": map[string]string{
			"id": "my-test-user-id",
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(authFilePath, authJSON, 0644); err != nil {
		t.Fatalf("failed to write reliant-auth.json: %v", err)
	}

	userID, err = getUserIDFromAuthFile()
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if userID != "my-test-user-id" {
		t.Errorf("expected 'my-test-user-id', got: %s", userID)
	}
}
