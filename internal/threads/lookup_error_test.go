// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/dbconn"
)

// latestCWRepo is a Repository whose GetLatestContextWindow fails with err.
// Any other method panics (nil embedded interface): SaveMessage must not get
// past the lookup.
type latestCWRepo struct {
	Repository
	err error
}

func (r latestCWRepo) GetLatestContextWindow(context.Context, string) (*db.ContextWindow, error) {
	return nil, r.err
}

// TestSaveMessage_OutageIsNotAMissingThread is the reliant#2100 mislabel: on
// 2026-11-02 PgBouncer's cached login failure reached SaveMessage's thread
// lookup and came out as "thread … does not exist - must be created before
// saving messages", which the activity layer classified as terminal. A
// database that did not answer says nothing about the thread.
func TestSaveMessage_OutageIsNotAMissingThread(t *testing.T) {
	t.Parallel()
	opts := SaveMessageOpts{
		ChatID:  "chat-1",
		Thread:  "thread-1",
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content: "hello",
	}

	t.Run("outage keeps its cause and is not called missing", func(t *testing.T) {
		t.Parallel()
		outage := &pgconn.PgError{
			Severity: "FATAL",
			Code:     "08P01",
			Message:  "server login has been failing, cached error: server conn crashed? (server_login_retry)",
		}
		svc := NewService(latestCWRepo{err: errors.Join(errors.New("failed to get latest context window"), outage)})
		_, err := svc.SaveMessage(context.Background(), opts)
		if err == nil {
			t.Fatal("SaveMessage succeeded with the database down")
		}
		for _, missing := range []string{"does not exist", "not found"} {
			if strings.Contains(strings.ToLower(err.Error()), missing) {
				t.Fatalf("a database outage was reported as a missing thread (%q): %v", missing, err)
			}
		}
		if !dbconn.Unavailable(err) {
			t.Fatalf("the outage's cause was lost from the chain: %v", err)
		}
	})

	t.Run("a genuinely missing thread still says so", func(t *testing.T) {
		t.Parallel()
		svc := NewService(latestCWRepo{err: sql.ErrNoRows})
		_, err := svc.SaveMessage(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "thread thread-1 does not exist") {
			t.Fatalf("want a missing-thread error, got %v", err)
		}
	})
}
