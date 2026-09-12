// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storedPNG seeds the fake repo with an image attachment owned by the user the
// test context carries. pngBytes (generate_image_test.go) starts with 0x89,
// which is not valid UTF-8 — that is the whole point of using it here too.
func storedPNG(t *testing.T, repo *fakeAttachmentRepo, id, userID string) *db.Attachment {
	t.Helper()
	att := &db.Attachment{
		ID:             id,
		UserID:         userID,
		Filename:       "generated-abc12345.png",
		Size:           int64(len(pngBytes)),
		MimeType:       "image/png",
		FilePath:       filepath.Join(userID, "generated-abc12345.png"),
		AttachmentType: string(attachment.TypeImage),
		Content:        pngBytes,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, repo.CreateAttachment(context.Background(), att))
	return att
}

func saveAttachmentCtx(t *testing.T) *rctx.ToolContext {
	t.Helper()
	ctx := createTestContext(t, "")
	ctx.Project = &db.Project{Path: "/workspace"}
	return ctx
}

// TestSaveAttachment_WritesStoredBytesToDaemon is the whole point of the tool:
// bytes that already exist reach the filesystem byte-for-byte, without being
// regenerated and without passing through the model's context.
//
// The exact-bytes assertion plus the textWrites==0 assertion is what catches a
// regression to WriteFile, which carries content as a JSON string on the remote
// path and silently replaces the 0x89 PNG signature with U+FFFD.
func TestSaveAttachment_WritesStoredBytesToDaemon(t *testing.T) {
	repo := newFakeAttachmentRepo()
	storedPNG(t, repo, "att-1", "test-user")

	tool := &saveAttachmentTool{repo: repo}
	recorder := &recordingDaemon{}
	ctx := saveAttachmentCtx(t).WithDaemon(recorder)

	resp, err := tool.Execute(ctx, SaveAttachmentParams{
		AttachmentID: "att-1",
		SaveTo:       "assets/bike.png",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)

	assert.Equal(t, 0, recorder.textWrites,
		"attachment bytes must go through WriteBinaryFile; the text write path corrupts them silently")
	assert.Equal(t, pngBytes, recorder.content, "binary must survive the write path unchanged")
	assert.Equal(t, filepath.Join("/workspace", "assets/bike.png"), recorder.path,
		"a relative save_to must resolve against the working directory")

	// The model sent a relative path, so echoing it back tells it nothing. The
	// resolved absolute path is what it needs to reference the file later.
	var output SaveAttachmentOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	assert.Equal(t, recorder.path, output.SavedTo)
	assert.Equal(t, "att-1", output.AttachmentID)
	assert.Equal(t, "image/png", output.MimeType)
	assert.Equal(t, len(pngBytes), output.Size)
	assert.Contains(t, resp.Content, recorder.path, "the resolved path must be reported to the model")

	// The bytes must NOT come back as a binary part. A save is a byte move, not
	// a read: re-inlining a multi-megabyte image the model has already seen is
	// exactly the waste this tool exists to avoid.
	assert.Empty(t, resp.BinaryParts, "save_attachment must not pull attachment bytes into the conversation")
}

// TestSaveAttachment_AbsolutePathBypassesWorkingDir pins that an absolute
// save_to is used as given, matching generate_image's save_to semantics.
func TestSaveAttachment_AbsolutePathBypassesWorkingDir(t *testing.T) {
	repo := newFakeAttachmentRepo()
	storedPNG(t, repo, "att-1", "test-user")

	tool := &saveAttachmentTool{repo: repo}
	recorder := &recordingDaemon{}
	ctx := saveAttachmentCtx(t).WithDaemon(recorder)

	resp, err := tool.Execute(ctx, SaveAttachmentParams{
		AttachmentID: "att-1",
		SaveTo:       "/tmp/elsewhere/bike.png",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)
	assert.Equal(t, "/tmp/elsewhere/bike.png", recorder.path)
	assert.Equal(t, pngBytes, recorder.content)
}

// TestSaveAttachment_Errors covers the failures a model can act on. Each must
// say something DIFFERENT, because "it didn't work" leaves the model to guess
// between retrying, fixing the id, and giving up.
func TestSaveAttachment_Errors(t *testing.T) {
	t.Run("attachment not found", func(t *testing.T) {
		tool := &saveAttachmentTool{repo: newFakeAttachmentRepo()}
		ctx := saveAttachmentCtx(t).WithDaemon(&recordingDaemon{})

		resp, err := tool.Execute(ctx, SaveAttachmentParams{AttachmentID: "nope", SaveTo: "out.png"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "not found")
		assert.Contains(t, resp.Content, "nope", "the error must name the id that failed")
	})

	t.Run("attachment belongs to another user", func(t *testing.T) {
		repo := newFakeAttachmentRepo()
		storedPNG(t, repo, "att-other", "someone-else")

		tool := &saveAttachmentTool{repo: repo}
		recorder := &recordingDaemon{}
		ctx := saveAttachmentCtx(t).WithDaemon(recorder)

		resp, err := tool.Execute(ctx, SaveAttachmentParams{AttachmentID: "att-other", SaveTo: "out.png"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "different user")
		assert.Nil(t, recorder.content, "another user's bytes must never reach the filesystem")
	})

	t.Run("no daemon connected", func(t *testing.T) {
		repo := newFakeAttachmentRepo()
		storedPNG(t, repo, "att-1", "test-user")

		tool := &saveAttachmentTool{repo: repo}
		resp, err := tool.Execute(saveAttachmentCtx(t), SaveAttachmentParams{
			AttachmentID: "att-1", SaveTo: "out.png",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "daemon")
	})

	t.Run("write fails", func(t *testing.T) {
		repo := newFakeAttachmentRepo()
		storedPNG(t, repo, "att-1", "test-user")

		tool := &saveAttachmentTool{repo: repo}
		// The oversize-transport rejection arrives as an ordinary write error.
		// It must reach the model verbatim: a generic "save failed" would hide
		// the one detail that explains why a big image and a small one behave
		// differently.
		ctx := saveAttachmentCtx(t).WithDaemon(&recordingDaemon{
			err: fmt.Errorf("request too large (2.9 MB exceeds the 1.0 MB transport limit)"),
		})

		resp, err := tool.Execute(ctx, SaveAttachmentParams{AttachmentID: "att-1", SaveTo: "out.png"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "transport limit",
			"the underlying write error must surface, not be flattened into a generic failure")
	})

	t.Run("attachment has no stored content", func(t *testing.T) {
		repo := newFakeAttachmentRepo()
		require.NoError(t, repo.CreateAttachment(context.Background(), &db.Attachment{
			ID: "ref-1", UserID: "test-user", Filename: "notes.txt",
			AttachmentType: string(attachment.TypeFileReference),
		}))

		tool := &saveAttachmentTool{repo: repo}
		ctx := saveAttachmentCtx(t).WithDaemon(&recordingDaemon{})

		resp, err := tool.Execute(ctx, SaveAttachmentParams{AttachmentID: "ref-1", SaveTo: "out.txt"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "no stored content")
	})

	t.Run("missing params", func(t *testing.T) {
		repo := newFakeAttachmentRepo()
		storedPNG(t, repo, "att-1", "test-user")
		tool := &saveAttachmentTool{repo: repo}
		ctx := saveAttachmentCtx(t).WithDaemon(&recordingDaemon{})

		resp, err := tool.Execute(ctx, SaveAttachmentParams{SaveTo: "out.png"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "attachment_id is required")

		resp, err = tool.Execute(ctx, SaveAttachmentParams{AttachmentID: "att-1"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "save_to is required")
	})

	t.Run("no repository", func(t *testing.T) {
		tool := &saveAttachmentTool{}
		resp, err := tool.Execute(saveAttachmentCtx(t), SaveAttachmentParams{
			AttachmentID: "att-1", SaveTo: "out.png",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "unavailable")
	})
}

// TestSaveAttachment_Registration pins the registry facts that decide who gets
// this tool. Unlike generate_image it IS in tag:default: it costs nothing, and
// withholding it is precisely what forced a model to regenerate an image it
// already held.
func TestSaveAttachment_Registration(t *testing.T) {
	t.Parallel()

	var found *ToolDefinition
	for i, def := range GetToolRegistry() {
		if def.Name == ToolSaveAttachment {
			found = &GetToolRegistry()[i]
			break
		}
	}
	require.NotNil(t, found, "save_attachment must be registered")
	assert.Equal(t, ToolRunsOnServer, found.RunsOn,
		"the bytes live in the database, which the daemon cannot reach")
	assert.Contains(t, found.Tags, TagDefault,
		"saving an existing attachment is free and is the only alternative to regenerating")
	assert.NotContains(t, found.Tags, TagReadOnly,
		"it writes a file to the user's disk")

	assert.Contains(t, ExpandToolFilter([]string{"tag:default"}, nil), ToolSaveAttachment)
}

// TestSaveAttachment_ParamSchema pins the agent-visible contract, including
// that both identifying parameters are required — a save with either one
// missing has no sensible default to fall back on.
func TestSaveAttachment_ParamSchema(t *testing.T) {
	t.Parallel()
	schema := NewSaveAttachmentTool(nil).ParamSchema()
	require.NotNil(t, schema.Properties)

	var names []string
	for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
		names = append(names, pair.Key)
	}
	assert.ElementsMatch(t, []string{"attachment_id", "save_to", "repo"}, names)
	assert.ElementsMatch(t, []string{"attachment_id", "save_to"}, schema.Required)
}

// TestSaveAttachment_RunsThroughTheWrapper exercises the registered tool end to
// end over its JSON boundary, which is how the executor actually calls it.
func TestSaveAttachment_RunsThroughTheWrapper(t *testing.T) {
	repo := newFakeAttachmentRepo()
	storedPNG(t, repo, "att-1", "test-user")

	recorder := &recordingDaemon{}
	ctx := saveAttachmentCtx(t).WithDaemon(recorder)

	resp, err := NewSaveAttachmentTool(repo).Run(ctx, ToolCall{
		ID:    "c1",
		Input: `{"attachment_id":"att-1","save_to":"assets/bike.png"}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)
	assert.Equal(t, pngBytes, recorder.content)
	assert.Equal(t, filepath.Join("/workspace", "assets/bike.png"), recorder.path)
}
