// Copyright (c) 2025 Reliant Labs

package provideraccounts_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

// env is one test's database, its credential vault and the store over both.
type env struct {
	provideraccounts.Service
	repo  *db.Repo
	vault vault.Service
}

// newEnv returns the provider-account store over a database of the test's own, and the raw pool
// for the tests that corrupt or inspect rows.
func newEnv(t *testing.T) (*env, *sql.DB) {
	t.Helper()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	v := db.NewTestVault(t, repo)
	svc, err := provideraccounts.New(provideraccounts.Deps{DB: repo.ORM(), Sealer: v})
	if err != nil {
		t.Fatalf("provideraccounts.New: %v", err)
	}
	return &env{Service: svc, repo: repo, vault: v}, raw
}

func createTestChat(t *testing.T, e *env) string {
	t.Helper()
	chatID := uuid.New().String()
	now := time.Now()
	if err := e.repo.CreateChat(context.Background(), &db.Chat{
		ID: chatID, Title: "Test Chat", ProjectID: "test-project", UserID: "test-user",
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	return chatID
}

func insertTestThread(t *testing.T, e *env, id, chatID, workflowID string) {
	t.Helper()
	wf := workflowID
	if _, err := e.repo.CreateThread(context.Background(), &db.Thread{
		ID: id, ChatID: chatID, WorkflowID: &wf, CreatedAt: time.Now(), Status: db.ThreadStatusRunning,
	}); err != nil {
		t.Fatalf("insert thread %s: %v", id, err)
	}
}
