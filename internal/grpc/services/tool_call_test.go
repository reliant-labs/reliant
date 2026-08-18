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
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

// fakeDaemonRouter implements toolexec.DaemonRouter for test assertions.
type fakeDaemonRouter struct {
	cancelCalls     []forwardCancelCall
	backgroundCalls []forwardBackgroundCall
	backgroundErr   error
}

type forwardCancelCall struct {
	userID    string
	requestID string
	reason    string
}

type forwardBackgroundCall struct {
	userID     string
	requestID  string
	toolCallID string
}

func (f *fakeDaemonRouter) IsDaemonOnline(_ context.Context, userID string) (bool, error) {
	return true, nil
}
func (f *fakeDaemonRouter) SendToolRequest(ctx context.Context, userID string, request *toolexec.ToolExecutionRequest) error {
	return nil
}
func (f *fakeDaemonRouter) SendToolExecutionCancel(_ context.Context, userID, requestID, reason string) error {
	f.cancelCalls = append(f.cancelCalls, forwardCancelCall{userID: userID, requestID: requestID, reason: reason})
	return nil
}
func (f *fakeDaemonRouter) SendToolExecutionBackground(_ context.Context, userID, requestID, toolCallID string) error {
	f.backgroundCalls = append(f.backgroundCalls, forwardBackgroundCall{userID: userID, requestID: requestID, toolCallID: toolCallID})
	return f.backgroundErr
}
func (f *fakeDaemonRouter) SendKillProcess(ctx context.Context, userID, processID string) error {
	return nil
}
func (f *fakeDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return f.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (f *fakeDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return nil, nil
}
func (f *fakeDaemonRouter) SendToolRequestSync(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (f *fakeDaemonRouter) SendToolRequestSyncWithSelector(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (f *fakeDaemonRouter) SendLoadProjectConfigs(_ context.Context, _, _ string, _ string) error {
	return nil
}
func (f *fakeDaemonRouter) SendWatchProjectConfigs(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}
func (f *fakeDaemonRouter) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (f *fakeDaemonRouter) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}
func (f *fakeDaemonRouter) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (f *fakeDaemonRouter) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	ch := make(chan *toolexec.ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (f *fakeDaemonRouter) Close() error { return nil }
func (f *fakeDaemonRouter) EnqueueDaemonCommand(_ context.Context, _, _ string, _ []byte, _ int32) (int, error) {
	return 0, nil
}
func (f *fakeDaemonRouter) ResolveDaemonID(_ context.Context, _ string) (string, error) {
	return "test-daemon-id", nil
}

func TestCancelToolCall_DeliversCancelToDaemonAndSucceeds(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	blockID := uuid.New().String()
	toolCallID := "tool-call-1"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "Cancel test",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              messageID,
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         blockID,
		MessageID:  messageID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		Version:    toolCallTestIntPtr(1),
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	router := &fakeDaemonRouter{}
	svc := NewToolCallService(repo, nil, router)

	resp, err := svc.CancelToolCall(ctx, connect.NewRequest(&reliantv1.CancelToolCallRequest{ToolCallId: toolCallID}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	require.Len(t, router.cancelCalls, 1)
	require.Equal(t, toolCallID, router.cancelCalls[0].requestID)
}

func toolCallTestIntPtr(v int) *int { return &v }
