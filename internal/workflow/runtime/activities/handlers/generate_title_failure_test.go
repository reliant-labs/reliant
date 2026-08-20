// Copyright (c) 2025 Reliant Labs
//
// Regression tests for how title generation FAILS.
//
// The activity used to catch an LLM error, substitute the truncated first
// message, and return success. That made a total provider outage look
// identical to normal operation: every chat was titled with the user's own
// text, nothing retried, and the only trace was a WARN line. A brotli
// content-encoding bug kept every one of these calls failing for a long time
// behind exactly that mask.
//
// The contract now: the activity reports failure, and the WORKFLOW decides to
// retry and when to settle for the fallback.
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// failingTitleDriver fails SendMessages the way a provider outage does.
type failingTitleDriver struct {
	mockLLMDriverForIdempotency
	err   error
	calls int
}

func (d *failingTitleDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) (*llm.DriverResponse, error) {
	d.calls++
	return nil, d.err
}

func failingTitleResolver(d *failingTitleDriver) drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, o ...llm.DriverOption) (llm.Driver, error) {
		var opts llm.DriverOptions
		for _, apply := range o {
			apply(&opts)
		}
		return d, nil
	}
}

// seedTitleChat creates a chat with no title, as CreateChat leaves it.
func seedTitleChat(t *testing.T, repo db.Repository) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return chatID
}

// The core regression: an LLM failure must surface as an activity error, NOT a
// silent success carrying the user's own text. This is the exact shape of the
// brotli decode failure that hid in production.
func TestGenerateTitle_LLMFailureReturnsErrorInsteadOfSilentFallback(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := seedTitleChat(t, repo)

	driver := &failingTitleDriver{
		err: errors.New("error parsing response json: invalid character '\\x03' looking for beginning of value"),
	}
	activity := &GenerateTitleActivity{repo: repo, driverResolver: failingTitleResolver(driver)}

	firstMessage := "creating titles of chats is broken, it's just using my text"
	_, err := activity.Execute(context.Background(), GenerateTitleInput{
		ChatID:       chatID,
		FirstMessage: firstMessage,
	})

	require.Error(t, err, "an LLM failure must fail the activity so Temporal retries")
	assert.Contains(t, err.Error(), "invalid character")

	// Nothing may be persisted on a failed attempt — a retry must still see an
	// untitled chat, since the activity returns early once a title exists.
	chat, getErr := repo.GetChat(context.Background(), chatID)
	require.NoError(t, getErr)
	assert.Empty(t, chat.Title, "a failed attempt must not persist a title")
	assert.NotEqual(t, firstMessage, chat.Title)
}

// The fallback still exists, but only when the caller asks for it explicitly.
func TestGenerateTitle_UseFallbackWritesTruncatedFirstMessage(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := seedTitleChat(t, repo)

	// The driver must never be consulted on the fallback path.
	driver := &failingTitleDriver{err: errors.New("must not be called")}
	activity := &GenerateTitleActivity{repo: repo, driverResolver: failingTitleResolver(driver)}

	out, err := activity.Execute(context.Background(), GenerateTitleInput{
		ChatID:       chatID,
		FirstMessage: "creating titles of chats is broken",
		UseFallback:  true,
	})

	require.NoError(t, err)
	assert.Equal(t, "creating titles of chats is broken", out.Title)
	assert.Zero(t, driver.calls, "fallback must not call the LLM")

	chat, getErr := repo.GetChat(context.Background(), chatID)
	require.NoError(t, getErr)
	assert.Equal(t, "creating titles of chats is broken", chat.Title)
}

// A model that returns only whitespace must fail rather than persist an empty
// title: the activity returns early when a title is set, so an empty one would
// leave the chat permanently untitled.
func TestGenerateTitle_EmptyGeneratedTitleIsAnError(t *testing.T) {
	repo := setupTestRepo(t)
	chatID := seedTitleChat(t, repo)

	d := &titleDriver{resp: setTitleCall(`{"title":"   "}`)}
	var opts llm.DriverOptions
	activity := &GenerateTitleActivity{repo: repo, driverResolver: titleResolver(d, &opts)}

	_, err := activity.Execute(context.Background(), GenerateTitleInput{
		ChatID:       chatID,
		FirstMessage: "some first message",
	})

	require.Error(t, err, "a blank title must not be persisted")

	chat, getErr := repo.GetChat(context.Background(), chatID)
	require.NoError(t, getErr)
	assert.Empty(t, chat.Title)
}

// An already-titled chat short-circuits before any LLM call, so a retry or a
// duplicate workflow never overwrites a good title.
func TestGenerateTitle_AlreadyTitledChatSkipsLLM(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID := seedTitleChat(t, repo)

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	chat.Title = "Existing Title"
	require.NoError(t, repo.UpdateChat(ctx, chat))

	driver := &failingTitleDriver{err: errors.New("must not be called")}
	activity := &GenerateTitleActivity{repo: repo, driverResolver: failingTitleResolver(driver)}

	out, err := activity.Execute(ctx, GenerateTitleInput{
		ChatID:       chatID,
		FirstMessage: "some first message",
	})

	require.NoError(t, err)
	assert.Equal(t, "Existing Title", out.Title)
	assert.Zero(t, driver.calls)
}
