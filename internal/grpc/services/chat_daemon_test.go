package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetChatDaemon_SetAndClear(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID := "test-project-daemon-" + uuid.NewString()
	now := time.Now().UTC()

	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Daemon Test Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     "Test Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: now,
		UpdatedAt: now,
	}))

	service := &ChatService{database: repo}

	// Set daemon
	daemonID := uuid.NewString()
	resp, err := service.SetChatDaemon(ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{
		ChatId:   chatID,
		DaemonId: daemonID,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)
	assert.Equal(t, chatID, resp.Msg.Chat.Id)
	assert.NotNil(t, resp.Msg.Chat.ActiveDaemonId)
	assert.Equal(t, daemonID, *resp.Msg.Chat.ActiveDaemonId)

	// Verify it persisted
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, daemonID, *chat.ActiveDaemonID)

	// Clear daemon (empty string)
	resp, err = service.SetChatDaemon(ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{
		ChatId:   chatID,
		DaemonId: "",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Chat)
	assert.Nil(t, resp.Msg.Chat.ActiveDaemonId)

	// Verify cleared
	chat, err = repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
}

func TestSetChatDaemon_ChatNotFound(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	service := &ChatService{database: repo}

	_, err := service.SetChatDaemon(ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{
		ChatId:   "nonexistent-chat-id",
		DaemonId: "some-daemon",
	}))
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestSetChatDaemon_MissingChatID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	service := &ChatService{database: repo}

	_, err := service.SetChatDaemon(ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{
		ChatId:   "",
		DaemonId: "some-daemon",
	}))
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

func TestSetChatDaemon_WrongOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	now := time.Now().UTC()
	projectID := "test-project-daemon-owner-" + uuid.NewString()

	// Create project and chat owned by "user-a"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-a")
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "user-a",
		Name:       "Owner Test",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		UserID:    "user-a",
		Title:     "User A Chat",
		ProjectID: projectID,
		State:     db.ChatStateIdle,
		CreatedAt: now,
		UpdatedAt: now,
	}))

	// Try to set daemon as "user-b"
	ctxB := context.WithValue(context.Background(), auth.UserIDContextKey, "user-b")
	service := &ChatService{database: repo}

	_, err := service.SetChatDaemon(ctxB, connect.NewRequest(&reliantv1.SetChatDaemonRequest{
		ChatId:   chatID,
		DaemonId: "some-daemon",
	}))
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

func TestInjectSessionDaemonID(t *testing.T) {
	daemonID := "test-daemon-123"

	t.Run("injects when set", func(t *testing.T) {
		inputs := make(map[string]interface{})
		chat := &db.Chat{ActiveDaemonID: &daemonID}
		injectSessionDaemonID(inputs, chat)
		assert.Equal(t, daemonID, inputs["session_daemon_id"])

		// The preview URL is deliberately NOT threaded through workflow inputs.
		// A handoff/terminal node runs inside the session daemon and discovers its
		// own preview URL at runtime (`reliant preview-url <port>` /
		// RELIANT_PREVIEW_URL_TEMPLATE), so nothing preview-related is injected here.
		_, exists := inputs["preview_url_template"]
		assert.False(t, exists, "preview_url_template must not be injected into workflow inputs")
	})

	t.Run("skips when nil", func(t *testing.T) {
		inputs := make(map[string]interface{})
		chat := &db.Chat{ActiveDaemonID: nil}
		injectSessionDaemonID(inputs, chat)
		_, exists := inputs["session_daemon_id"]
		assert.False(t, exists)
	})

	t.Run("skips when empty", func(t *testing.T) {
		inputs := make(map[string]interface{})
		empty := ""
		chat := &db.Chat{ActiveDaemonID: &empty}
		injectSessionDaemonID(inputs, chat)
		_, exists := inputs["session_daemon_id"]
		assert.False(t, exists)
	})

	t.Run("skips when chat is nil", func(t *testing.T) {
		inputs := make(map[string]interface{})
		injectSessionDaemonID(inputs, nil)
		_, exists := inputs["session_daemon_id"]
		assert.False(t, exists)
	})
}
