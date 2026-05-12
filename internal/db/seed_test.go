// Copyright (c) 2025 Reliant Labs
package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
)

func TestSeedAPIKeyFromEnv(t *testing.T) {
	// TODO: convert from SQLite to Postgres — seedAPIKeyFromEnv() uses ? placeholders
	// which are SQLite-specific. The function itself needs to be updated to use $N
	// placeholders or go through the Repo layer before this test can run against Postgres.
	t.Skip("TODO: convert from SQLite to Postgres — seedAPIKeyFromEnv uses SQLite ? placeholders")
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
