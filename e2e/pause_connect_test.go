// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
)

// authInjectInterceptor injects the test user ID into the request context
// so the Connect handler's auth.MustGetUserID works without JWT validation.
type authInjectInterceptor struct {
	userID string
}

func (a *authInjectInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = context.WithValue(ctx, auth.UserIDContextKey, a.userID)
		return next(ctx, req)
	}
}

func (a *authInjectInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInjectInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// newConnectTestServer creates an httptest.Server hosting the ChatService Connect handler
// and returns a typed ChatServiceClient pointing at it.
// This tests the full HTTP routing path: client -> HTTP -> Connect mux -> handler.
func (h *TestHarness) newConnectTestServer(t *testing.T) (reliantv1connect.ChatServiceClient, func()) {
	t.Helper()

	// Register the ChatService handler with an interceptor that injects the test user ID.
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewChatServiceHandler(
		h.chatService,
		connect.WithInterceptors(&authInjectInterceptor{userID: h.userID}),
	)
	mux.Handle(path, handler)

	ts := httptest.NewServer(mux)

	client := reliantv1connect.NewChatServiceClient(
		ts.Client(),
		ts.URL,
	)

	return client, ts.Close
}

// TestPauseChat_ConnectHTTPRouting verifies that PauseChat and ResumeChat RPCs
// are properly registered in the Connect HTTP handler and routable through the
// generated client.
//
// Regression test: commit 4ab14af15 ("more bug fixes #1752") accidentally removed
// the rpc PauseChat/ResumeChat declarations from the proto service block. The
// message types and Go handler methods survived, but the Connect codegen stopped
// generating HTTP routing entries, so the frontend's client.pauseChat() was
// undefined at runtime and calls silently failed.
func TestPauseChat_ConnectHTTPRouting(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	client, closeServer := h.newConnectTestServer(t)
	defer closeServer()

	// --- Mock: simple single-response LLM so the workflow completes quickly ---
	h.MockLLM.SetResponse("Hello from pause routing test")

	// Start a workflow so we have a valid chat with a workflow ID
	chatID := h.StartAgentWorkflowViaGRPC(t, "test pause routing")

	// Wait for the workflow to produce at least a user + assistant message
	h.WaitForMessages(t, chatID, 2)

	// Wait for the workflow to complete (simple single response, no tools)
	h.WaitForWorkflowComplete(t, chatID)

	// --- Test PauseChat is routable ---
	// The workflow already completed, so PauseChat will likely fail with a
	// Temporal "workflow not running" error. That's fine — the important thing
	// is that we do NOT get a "not implemented" or routing error. We verify
	// the Connect error code is NOT CodeUnimplemented.
	pauseResp, pauseErr := client.PauseChat(context.Background(), connect.NewRequest(&reliantv1.PauseChatRequest{
		ChatId: chatID,
	}))

	if pauseErr != nil {
		// The call reached the handler (not "unimplemented") — this is the key assertion.
		// Before the fix, (client as any).pauseChat was undefined in JS, and in Go
		// the Connect mux would return CodeUnimplemented.
		require.NotEqual(t, connect.CodeUnimplemented, connect.CodeOf(pauseErr),
			"PauseChat should be routed to the handler, not return Unimplemented. Error: %v", pauseErr)
		t.Logf("PauseChat returned expected error (workflow already completed): %v", pauseErr)
	} else {
		require.True(t, pauseResp.Msg.Success, "PauseChat should succeed")
		t.Log("PauseChat succeeded (workflow was still running)")
	}

	// --- Test ResumeChat is routable ---
	resumeResp, resumeErr := client.ResumeChat(context.Background(), connect.NewRequest(&reliantv1.ResumeChatRequest{
		ChatId: chatID,
	}))

	if resumeErr != nil {
		require.NotEqual(t, connect.CodeUnimplemented, connect.CodeOf(resumeErr),
			"ResumeChat should be routed to the handler, not return Unimplemented. Error: %v", resumeErr)
		t.Logf("ResumeChat returned expected error (workflow already completed): %v", resumeErr)
	} else {
		require.True(t, resumeResp.Msg.Success, "ResumeChat should succeed")
		t.Log("ResumeChat succeeded")
	}
}

// TestPauseChat_ConnectHTTPRouting_InvalidChat verifies that PauseChat returns
// a proper error (not CodeUnimplemented) when called with a nonexistent chat.
func TestPauseChat_ConnectHTTPRouting_InvalidChat(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	client, closeServer := h.newConnectTestServer(t)
	defer closeServer()

	// Call PauseChat with a bogus chat ID — should get NotFound, not Unimplemented
	_, err := client.PauseChat(context.Background(), connect.NewRequest(&reliantv1.PauseChatRequest{
		ChatId: "nonexistent-chat-id",
	}))
	require.Error(t, err, "PauseChat with invalid chat should return an error")
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"PauseChat with invalid chat should return NotFound, got: %v", err)

	// Same for ResumeChat
	_, err = client.ResumeChat(context.Background(), connect.NewRequest(&reliantv1.ResumeChatRequest{
		ChatId: "nonexistent-chat-id",
	}))
	require.Error(t, err, "ResumeChat with invalid chat should return an error")
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"ResumeChat with invalid chat should return NotFound, got: %v", err)
}

// TestPauseAndResume_ViaConnectHTTP tests the full pause/resume lifecycle
// through the Connect HTTP layer (not just direct Go method calls).
// This is the most realistic test — it exercises the same code path as the
// frontend: HTTP client -> Connect mux -> handler -> Temporal signal.
func TestPauseAndResume_ViaConnectHTTP(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	client, closeServer := h.newConnectTestServer(t)
	defer closeServer()

	ctx := context.Background()

	// --- Mock setup ---
	// Response 1: assistant returns a tool call.
	// The tool has a delay so we can pause mid-flight.
	// Response 2: final text answer after resume.
	h.MockLLM.SetResponseWithToolCall(
		"Let me run that command for you.",
		"Bash",
		map[string]interface{}{
			"command": "echo 'connect-pause-test'",
		},
	)
	h.MockLLM.AddResponse(MockResponse{
		Text: "The command completed. Here is the result of the connect pause test.",
	})

	h.MockTools.On("Bash", MockToolResponse{
		Result:  "connect-pause-test",
		Success: true,
		Delay:   500 * time.Millisecond,
	})

	// --- Start the workflow ---
	chatID := h.StartAgentWorkflowViaGRPC(t, "run echo connect-pause-test")
	t.Logf("Chat started: %s", chatID)

	// Wait for first assistant message (tool call)
	messages := h.WaitForMessages(t, chatID, 2)
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[1].Role)
	t.Log("First assistant message received (tool call)")

	// --- Pause via Connect HTTP client ---
	pauseResp, err := client.PauseChat(ctx, connect.NewRequest(&reliantv1.PauseChatRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err, "PauseChat via Connect HTTP should not error")
	require.True(t, pauseResp.Msg.Success, "PauseChat should report success")
	t.Log("Pause signal sent via Connect HTTP")

	// --- Verify no new messages while paused ---
	time.Sleep(400 * time.Millisecond)

	messagesPausedA, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	countA := len(messagesPausedA)

	time.Sleep(500 * time.Millisecond)

	messagesPausedB, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	countB := len(messagesPausedB)

	require.Equal(t, countA, countB,
		"message count should stabilize while workflow is paused")
	t.Logf("No additional messages while paused (count stable at %d)", countB)

	// --- Resume via Connect HTTP client ---
	resumeResp, err := client.ResumeChat(ctx, connect.NewRequest(&reliantv1.ResumeChatRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err, "ResumeChat via Connect HTTP should not error")
	require.True(t, resumeResp.Msg.Success, "ResumeChat should report success")
	t.Log("Resume signal sent via Connect HTTP")

	// --- Verify workflow completes after resume ---
	h.WaitForWorkflowComplete(t, chatID)

	finalMessages, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(finalMessages), 3,
		"should have at least user+assistant+tool messages after resume")

	// Verify first three messages: user, assistant(tool call), tool(result)
	h.AssertMessageRoles(t, finalMessages[:3],
		reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
	)

	t.Logf("Workflow completed after resume: %d messages total", len(finalMessages))
	t.Log("Full pause/resume lifecycle via Connect HTTP verified")
}
