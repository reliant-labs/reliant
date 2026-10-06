// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/videojobs"
)

// A job recorded under this call's id is resumed rather than submitted again —
// but only if it is THIS chat's job. A tool call id is chosen by the model
// provider and can repeat across chats; resuming another chat's job under a
// matching id would hand this chat that chat's clip.
func TestGenerateVideo_DoesNotResumeAnotherChatsJob(t *testing.T) {
	repo := newFakeAttachmentRepo()
	jobs := newFakeVideoJobs()
	require.NoError(t, jobs.Create(context.Background(), &videojobs.Job{
		ToolCallID: "tc-1", UserID: "test-user", ChatID: "chat-other", Driver: "gemini", ModelID: "veo-3.1-generate",
		APIModel: "veo-3.1-generate-preview", ProviderJob: "operations/another-chats-render", State: videojobs.StateSubmitted,
	}))
	generator := &fakeVideoGenerator{caps: veoCaps()}
	tool := newVideoTool(repo, jobs, generator)

	resp, err := tool.Execute(videoCtx(t, "tc-1"), GenerateVideoParams{Prompt: "a red ball bouncing, single continuous shot"})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error: %s", resp.Content)

	assert.Equal(t, 1, generator.submits, "this chat's call must start its own render")
	var output GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	assert.Equal(t, "operations/new-1", output.ProviderJob, "the clip must come from this chat's render, not another chat's")
}
