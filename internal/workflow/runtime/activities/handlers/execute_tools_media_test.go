// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/bmp"

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

func encodedPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func TestNormalizeToolMedia(t *testing.T) {
	t.Parallel()

	var bmpBytes bytes.Buffer
	require.NoError(t, bmp.Encode(&bmpBytes, image.NewGray(image.Rect(0, 0, 4, 3))))

	tests := []struct {
		name       string
		part       message.BinaryContent
		wantType   attachment.AttachmentType
		wantMIME   string
		wantReason string
	}{
		{name: "png passes", part: message.BinaryContent{MIMEType: "image/png", Data: encodedPNG(t, 8, 8)}, wantType: attachment.TypeImage, wantMIME: "image/png"},
		{name: "pdf is a document", part: message.BinaryContent{MIMEType: "application/pdf", Data: []byte("%PDF-1.4")}, wantType: attachment.TypeDocument, wantMIME: "application/pdf"},
		{name: "bmp becomes png", part: message.BinaryContent{Path: "/w/a.bmp", MIMEType: "image/bmp", Data: bmpBytes.Bytes()}, wantType: attachment.TypeImage, wantMIME: "image/png"},
		{name: "svg is refused with what to do", part: message.BinaryContent{MIMEType: "image/svg+xml", Data: []byte("<svg/>")}, wantReason: "read the file as text"},
		{name: "a full-page screenshot taller than 8000px is refused", part: message.BinaryContent{MIMEType: "image/png", Data: encodedPNG(t, 10, 8001)}, wantReason: "10x8001 px"},
		{name: "an image over the byte limit is refused", part: message.BinaryContent{MIMEType: "image/png", Data: make([]byte, maxModelImageBytes+1)}, wantReason: "downscale or crop"},
		{name: "corrupt image is refused", part: message.BinaryContent{MIMEType: "image/png", Data: []byte("not a png")}, wantReason: "not a readable image/png"},
		{name: "unknown type is refused", part: message.BinaryContent{MIMEType: "application/zip", Data: []byte("PK")}, wantReason: "not a type a model can read"},
		{name: "empty is refused", part: message.BinaryContent{MIMEType: "image/png"}, wantReason: "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, attType, reason := normalizeToolMedia(tc.part)
			if tc.wantReason != "" {
				assert.Contains(t, reason, tc.wantReason)
				return
			}
			require.Empty(t, reason)
			assert.Equal(t, tc.wantType, attType)
			assert.Equal(t, tc.wantMIME, got.MIMEType)
			if tc.wantMIME == "image/png" {
				_, err := png.DecodeConfig(bytes.NewReader(got.Data))
				assert.NoError(t, err, "what reaches the model must be a real PNG")
			}
		})
	}
}

// Each usable part becomes one attachment row the user owns; an unusable one
// becomes a sentence on the result instead of a request a provider refuses.
func TestPersistToolMedia(t *testing.T) {
	t.Parallel()

	var created []*db.Attachment
	store := &MockExecuteToolsStore{CreateAttachmentFunc: func(_ context.Context, a *db.Attachment) error {
		created = append(created, a)
		return nil
	}}
	a := &ExecuteToolsActivity{repo: store}
	tec := &toolExecutionContext{project: &db.Project{UserID: "user-1"}, toolName: "view", toolCallID: "call_1", chatID: "chat-1"}

	shot := encodedPNG(t, 64, 48)
	content, ids := a.persistToolMedia(context.Background(), tec, "Image file: shot.png (image/png, 1KB)", []message.BinaryContent{
		{Path: "/home/workspace/shots/shot.png", MIMEType: "image/png", Data: shot},
		{Path: "/home/workspace/logo.svg", MIMEType: "image/svg+xml", Data: []byte("<svg/>")},
	}, nil)

	require.Len(t, ids, 1)
	require.Len(t, created, 1)
	att := created[0]
	assert.Equal(t, ids[0], att.ID)
	assert.Equal(t, "user-1", att.UserID)
	assert.Equal(t, "shot.png", att.Filename)
	assert.Equal(t, "image/png", att.MimeType)
	assert.Equal(t, string(attachment.TypeImage), att.AttachmentType)
	assert.Equal(t, shot, att.Content)
	assert.Equal(t, int64(len(shot)), att.Size)
	assert.Contains(t, content, "Image file: shot.png")
	assert.Contains(t, content, "logo.svg is not shown: SVG")

	t.Run("a tool that stored its own media is not stored twice", func(t *testing.T) {
		created = nil
		owned := &ExecuteToolsActivity{repo: &MockExecuteToolsStore{
			GetAttachmentFunc: func(_ context.Context, id string) (*db.Attachment, error) {
				return &db.Attachment{ID: id, UserID: "user-1", Content: shot}, nil
			},
		}}
		content, ids := owned.persistToolMedia(context.Background(), tec, "Generated.", []message.BinaryContent{{MIMEType: "image/png", Data: shot}}, []string{"att-own"})
		assert.Equal(t, []string{"att-own"}, ids)
		assert.Equal(t, "Generated.", content)
	})

	t.Run("media beside a reported id is still persisted", func(t *testing.T) {
		created = nil
		store.GetAttachmentFunc = func(_ context.Context, id string) (*db.Attachment, error) {
			return &db.Attachment{ID: id, UserID: "user-1", Content: []byte("other bytes")}, nil
		}
		defer func() { store.GetAttachmentFunc = nil }()
		_, ids := a.persistToolMedia(context.Background(), tec, "", []message.BinaryContent{{Path: "/w/shot.png", MIMEType: "image/png", Data: shot}}, []string{"att-own"})
		require.Len(t, ids, 2)
		assert.Equal(t, "att-own", ids[0])
		require.Len(t, created, 1)
		assert.Equal(t, ids[1], created[0].ID)
	})

	t.Run("a reported id the user does not own is dropped", func(t *testing.T) {
		created = nil
		store.GetAttachmentFunc = func(_ context.Context, id string) (*db.Attachment, error) {
			switch id {
			case "att-theirs":
				return &db.Attachment{ID: id, UserID: "user-2", Content: shot}, nil
			case "att-gone":
				return nil, nil
			}
			return nil, errors.New("lookup failed")
		}
		defer func() { store.GetAttachmentFunc = nil }()
		_, ids := a.persistToolMedia(context.Background(), tec, "", []message.BinaryContent{{Path: "/w/shot.png", MIMEType: "image/png", Data: shot}}, []string{"att-theirs", "att-gone", "att-err"})
		require.Len(t, ids, 1, "only the freshly persisted part survives")
		require.Len(t, created, 1)
		assert.Equal(t, ids[0], created[0].ID)
		assert.Equal(t, "user-1", created[0].UserID)
	})

	t.Run("a failed save says so and keeps the result", func(t *testing.T) {
		failing := &ExecuteToolsActivity{repo: &MockExecuteToolsStore{CreateAttachmentFunc: func(context.Context, *db.Attachment) error {
			return errors.New("connection reset")
		}}}
		content, ids := failing.persistToolMedia(context.Background(), tec, "Image file: shot.png", []message.BinaryContent{{Path: "/w/shot.png", MIMEType: "image/png", Data: shot}}, nil)
		assert.Empty(t, ids)
		assert.Contains(t, content, "shot.png is not shown: it could not be saved")
	})

	t.Run("an MCP screenshot with no path is named after the tool", func(t *testing.T) {
		created = nil
		_, ids := a.persistToolMedia(context.Background(), &toolExecutionContext{toolName: "mcp__chrome-devtools__take_screenshot"}, "", []message.BinaryContent{{MIMEType: "image/png", Data: shot}}, nil)
		require.Len(t, ids, 1)
		assert.Equal(t, "mcp__chrome-devtools__take_screenshot-1.png", created[0].Filename)
	})
}

// The reported failure, end to end against a real database: the view tool
// returns a screenshot, and the next prompt CallLLM builds carries it as the
// tool result's image. Before persistToolMedia the bytes were dropped at the
// workflow boundary and the reloaded result held only its caption.
func TestToolResultImageReachesTheNextPrompt(t *testing.T) {
	testkit.Slow(t, "runs workflow activities against pgtest's Postgres")
	t.Parallel()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID, projectID, chatID := uuid.New().String(), uuid.New().String(), uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestThreadAndContextWindow(ctx, chatID, chatID)
	thread := chatID
	h.CreateTestUserMessage(ctx, chatID, thread)

	save := NewSaveMessageActivity(h.Repo())
	const callID = "toolu_view_1"
	var assistantOut SaveMessageOutput
	require.NoError(t, h.ExecuteActivity(save.Execute, &SaveMessageInput{
		ChatID: chatID, Thread: thread, Role: "assistant",
		ToolCalls: []ToolCall{{ID: callID, Name: "view", Input: `{"file_path":"../../g3-logs/obs-demo/shots/dashboard-end.png"}`}},
	}, &assistantOut))

	shot := encodedPNG(t, 1280, 720)
	executor := newMockToolExecutor()
	executor.results[callID] = &toolexec.ToolResult{
		Success: true,
		Content: "Image file: ../../g3-logs/obs-demo/shots/dashboard-end.png (image/png, 241.0KB)",
		BinaryParts: []message.BinaryContent{{
			Path: "/home/workspace/g3-logs/obs-demo/shots/dashboard-end.png", MIMEType: "image/png", Data: shot,
		}},
	}
	var execOut ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(NewExecuteToolsActivity(h.Repo(), executor).Execute, ExecuteToolsInput{
		ChatID: chatID, Thread: thread,
		ToolCalls: []ToolCall{{ID: callID, Name: "view", Input: `{"file_path":"x"}`}},
	}, &execOut))
	require.Len(t, execOut.ToolResults, 1)
	require.Len(t, execOut.ToolResults[0].GetAttachmentIds(), 1, "the screenshot must cross the workflow boundary as an attachment id")

	// The workflow saves the tool message from execute_tools' output.
	var toolOut SaveMessageOutput
	require.NoError(t, h.ExecuteActivity(save.Execute, &SaveMessageInput{
		ChatID: chatID, Thread: thread, Role: "tool",
		ToolResults: protoToolResultsToMessage(execOut.ToolResults),
	}, &toolOut))

	msgs, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
	require.NoError(t, err)
	var result *message.ToolResult
	for i := range msgs {
		for _, tr := range msgs[i].ToolResults() {
			if tr.ToolCallID == callID {
				tr := tr
				result = &tr
			}
		}
	}
	require.NotNil(t, result, "the view result must be in the reloaded history")
	assert.Contains(t, result.Content, "dashboard-end.png (image/png, 241.0KB)")
	require.Len(t, result.BinaryParts, 1, "the next prompt must carry the screenshot itself")
	assert.Equal(t, "image/png", result.BinaryParts[0].MIMEType)
	assert.Equal(t, shot, result.BinaryParts[0].Data)
	assert.Empty(t, ValidateToolPairing(msgs))
}
