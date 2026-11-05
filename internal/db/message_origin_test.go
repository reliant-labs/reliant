package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// send_to_run reaches another run through SignalRun, which is the human's own
// SendMessage path. The pr-fix hand-off of 2026-11-05 ("[pr-fix] Your PR … is
// stuck") was stored there with no sender and rendered as the owner's words.
// The origin on the context is what makes that save the sending run's.
func TestSaveMessageToThread_OriginAttributesTheMessageToTheSendingRun(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := setupTestRepo(t)
	defer cleanup()

	target := "chat-origin-target"
	sender := "chat-origin-pr-fix"
	createActivityTestChat(t, repo, target)
	createActivityTestChat(t, repo, sender)
	_, err := rawDB.Exec(`UPDATE chats SET title = $1 WHERE id = $2`, "pr-fix control-plane#2017", sender)
	require.NoError(t, err)

	ctx := WithMessageOrigin(context.Background(), MessageOrigin{ThreadID: sender, Subject: "PR stuck"})
	msg, err := repo.SaveMessageToThread(ctx, target, target,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "[pr-fix] Your PR is stuck", nil, nil, nil)
	require.NoError(t, err)

	stored, err := repo.GetMessage(context.Background(), msg.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.SenderThreadID, "a run's message must not be stored as the human's")
	assert.Equal(t, sender, *stored.SenderThreadID)
	require.NotNil(t, stored.SenderKind)
	assert.Equal(t, reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE, *stored.SenderKind,
		"an unset kind defaults to an agent message")
	require.NotNil(t, stored.SenderSubject)
	assert.Equal(t, "PR stuck", *stored.SenderSubject)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, stored.Role, "the model still reads it as input")

	payload := latestMessageUpdatePayload(t, rawDB, target, msg.ID)
	live, ok := payload["sender"].(map[string]any)
	require.True(t, ok, "the live update carries the sender, or it renders as a user bubble until reload")
	assert.Equal(t, sender, live["thread_id"])
	assert.Equal(t, "pr-fix control-plane#2017", live["title"], "a run's untitled main thread is titled by the run")
	assert.Equal(t, "PR stuck", live["subject"])
}

func TestSaveMessageToThread_OriginLeavesNonUserMessagesAlone(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	chatID := "chat-origin-system"
	createActivityTestChat(t, repo, chatID)

	ctx := WithMessageOrigin(context.Background(), MessageOrigin{ThreadID: chatID})
	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "note", nil, nil, nil)
	require.NoError(t, err)
	stored, err := repo.GetMessage(context.Background(), msg.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.SenderThreadID, "only the user-role message an agent sent is attributed")
}

func TestSenderTitles_ThreadTitleThenRunTitle(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	run := "chat-sender-titles-run"
	createActivityTestChat(t, repo, run)
	_, err := rawDB.Exec(`UPDATE chats SET title = $1 WHERE id = $2`, "Release captain", run)
	require.NoError(t, err)

	spawnTitle := "probe-A"
	_, err = repo.CreateThread(ctx, &Thread{ID: "thread-sender-titles-spawn", ChatID: run, ParentThreadID: &run, Title: &spawnTitle, CreatedAt: time.Now()})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &Thread{ID: "thread-sender-titles-untitled", ChatID: run, ParentThreadID: &run, CreatedAt: time.Now()})
	require.NoError(t, err)

	titles, err := repo.SenderTitles(ctx, []string{run, "thread-sender-titles-spawn", "thread-sender-titles-untitled", "missing"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		run:                          "Release captain",
		"thread-sender-titles-spawn": "probe-A",
	}, titles, "a sub-agent keeps its own title; an untitled sub-agent is not given its run's")
}

func TestWithMessageOrigin_EmptyThreadIsNoOrigin(t *testing.T) {
	t.Parallel()
	_, ok := MessageOriginFrom(WithMessageOrigin(context.Background(), MessageOrigin{Subject: "x"}))
	assert.False(t, ok)
	_, ok = MessageOriginFrom(context.Background())
	assert.False(t, ok)
}
