// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// imageFileDaemon serves files from memory through the two calls present_image
// makes. The embedded nil Client panics on anything else — in particular the
// text read path, which would corrupt image bytes.
type imageFileDaemon struct {
	daemon.Client
	files map[string][]byte
	// sizes overrides a file's reported size, to exercise the size cap
	// without allocating 20MB.
	sizes map[string]int64
	dirs  map[string]bool
}

func (d *imageFileDaemon) StatFile(_ context.Context, path string) (*daemon.FileStat, error) {
	if d.dirs[path] {
		return &daemon.FileStat{Exists: true, IsDir: true}, nil
	}
	data, ok := d.files[path]
	if !ok {
		return &daemon.FileStat{Exists: false}, nil
	}
	size := int64(len(data))
	if s, ok := d.sizes[path]; ok {
		size = s
	}
	return &daemon.FileStat{Exists: true, Size: size}, nil
}

func (d *imageFileDaemon) ReadBinaryFile(_ context.Context, path string, _ int64) ([]byte, error) {
	return d.files[path], nil
}

func encodedImage(t *testing.T, w, h int, encode func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, encode(&buf, img))
	return buf.Bytes()
}

func pngOf(t *testing.T, w, h int) []byte {
	return encodedImage(t, w, h, func(b *bytes.Buffer, i image.Image) error { return png.Encode(b, i) })
}

func jpegOf(t *testing.T, w, h int) []byte {
	return encodedImage(t, w, h, func(b *bytes.Buffer, i image.Image) error { return jpeg.Encode(b, i, nil) })
}

func presentImageRun(t *testing.T, d daemon.Client, params PresentImageParams) (*fakeAttachmentRepo, ToolResponse, PresentImageOutput) {
	t.Helper()
	repo := newFakeAttachmentRepo()
	tool := &presentImageTool{repo: repo}
	ctx := saveAttachmentCtx(t)
	if d != nil {
		ctx = ctx.WithDaemon(d)
	}
	resp, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	var output PresentImageOutput
	if resp.Metadata != "" {
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	}
	return repo, resp, output
}

// The whole point: a file on the machine becomes an attachment the timeline
// renders, typed so the replay never hands it to a model, with its id where
// execute_tools reads it (attachment_ids in the metadata).
func TestPresentImage_StoresTheFileAsAUserOnlyAttachment(t *testing.T) {
	t.Parallel()

	chart := pngOf(t, 640, 480)
	// A JPEG named .png: the type comes from the bytes, not the name.
	mislabelled := jpegOf(t, 32, 16)
	d := &imageFileDaemon{files: map[string][]byte{
		"/workspace/out/chart.png": chart,
		"/tmp/shot.png":            mislabelled,
	}}

	repo, resp, output := presentImageRun(t, d, PresentImageParams{
		Paths:   []string{"out/chart.png", "/tmp/shot.png"},
		Caption: "  p95 latency, last 24h ",
	})
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)
	assert.Empty(t, resp.BinaryParts, "the image is for the user; the model is not handed it")

	require.Len(t, output.Images, 2)
	require.Len(t, output.AttachmentIDs, 2)
	assert.Equal(t, "p95 latency, last 24h", output.Caption)

	first := output.Images[0]
	assert.Equal(t, "/workspace/out/chart.png", first.Path, "a relative path resolves against the working directory")
	assert.Equal(t, "image/png", first.MimeType)
	assert.Equal(t, 640, first.Width)
	assert.Equal(t, 480, first.Height)
	assert.Equal(t, "image/jpeg", output.Images[1].MimeType, "the type is sniffed from the bytes")

	att := repo.stored[first.AttachmentID]
	require.NotNil(t, att)
	assert.Equal(t, chart, att.Content, "bytes are stored unchanged")
	assert.Equal(t, string(attachment.TypeDisplay), att.AttachmentType, "presented images are user-only")
	assert.Equal(t, "test-user", att.UserID, "owned by the caller, so the ownership check on the result admits it")
	assert.Equal(t, "chart.png", att.Filename)
	assert.Equal(t, "/workspace/out/chart.png", att.SourcePath(),
		"the resolved source path is kept, so the client can open the file in the viewer")

	assert.Equal(t, output.AttachmentIDs, attachmentIDsFromMetadataForTest(t, resp.Metadata))
	assert.Contains(t, resp.Content, "Showed 2 images to the user")
	assert.Contains(t, resp.Content, "you cannot", "the model is told it has not seen the image")
	assert.Contains(t, resp.Content, "view")
}

// attachmentIDsFromMetadataForTest reads the metadata the way execute_tools
// does (attachmentIDsFromMetadata, in another package): the top-level
// attachment_ids array.
func attachmentIDsFromMetadataForTest(t *testing.T, metadata string) []string {
	t.Helper()
	var envelope struct {
		AttachmentIDs []string `json:"attachment_ids"`
	}
	require.NoError(t, json.Unmarshal([]byte(metadata), &envelope))
	return envelope.AttachmentIDs
}

// One bad path does not hide the good ones: they are shown, and the result
// names each skipped file with the reason.
func TestPresentImage_ShowsWhatItCanAndNamesWhatItSkipped(t *testing.T) {
	t.Parallel()

	d := &imageFileDaemon{
		files: map[string][]byte{
			"/workspace/ok.png":    pngOf(t, 4, 4),
			"/workspace/notes.png": []byte("this is text, not an image"),
			"/workspace/huge.png":  pngOf(t, 4, 4),
			"/workspace/logo.svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		},
		sizes: map[string]int64{"/workspace/huge.png": maxPresentImageBytes + 1},
		dirs:  map[string]bool{"/workspace/assets": true},
	}

	repo, resp, output := presentImageRun(t, d, PresentImageParams{
		Paths: []string{"ok.png", "missing.png", "notes.png", "huge.png", "logo.svg", "assets"},
	})
	require.False(t, resp.IsError, "a partial success is a success: %s", resp.Content)
	require.Len(t, output.Images, 1)
	assert.Len(t, repo.stored, 1, "nothing is stored for a skipped path")

	reasons := map[string]string{}
	for _, skip := range output.Skipped {
		reasons[skip.Path] = skip.Reason
	}
	assert.Contains(t, reasons["missing.png"], "not found")
	assert.Contains(t, reasons["notes.png"], "is not a PNG, JPEG, GIF or WebP")
	assert.Contains(t, reasons["huge.png"], "above the 20.0MB")
	assert.Contains(t, reasons["logo.svg"], "render it to PNG")
	assert.Contains(t, reasons["assets"], "directory")
	assert.Contains(t, resp.Content, "Not shown:")
}

func TestPresentImage_Errors(t *testing.T) {
	t.Parallel()

	t.Run("nothing shown is an error naming every reason", func(t *testing.T) {
		t.Parallel()
		_, resp, _ := presentImageRun(t, &imageFileDaemon{files: map[string][]byte{}}, PresentImageParams{Paths: []string{"a.png"}})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "Nothing was shown")
		assert.Contains(t, resp.Content, "a.png: file not found")
	})
	t.Run("no paths", func(t *testing.T) {
		t.Parallel()
		_, resp, _ := presentImageRun(t, &imageFileDaemon{}, PresentImageParams{Paths: []string{" "}})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "paths is required")
	})
	t.Run("too many paths", func(t *testing.T) {
		t.Parallel()
		paths := make([]string, maxPresentImages+1)
		for i := range paths {
			paths[i] = "x.png"
		}
		_, resp, _ := presentImageRun(t, &imageFileDaemon{}, PresentImageParams{Paths: paths})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "at most 10")
	})
	t.Run("no daemon", func(t *testing.T) {
		t.Parallel()
		_, resp, _ := presentImageRun(t, nil, PresentImageParams{Paths: []string{"a.png"}})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "requires a connected daemon")
	})
	t.Run("no store", func(t *testing.T) {
		t.Parallel()
		resp, err := (&presentImageTool{}).Execute(saveAttachmentCtx(t), PresentImageParams{Paths: []string{"a.png"}})
		require.NoError(t, err)
		require.True(t, resp.IsError)
	})
}

// Registered where a coding agent finds it from turn 1, plan mode included,
// and classified as reaching the machine, so a no-machine run never offers it.
func TestPresentImage_Registration(t *testing.T) {
	t.Parallel()

	var def *ToolDefinition
	for _, d := range GetToolRegistry() {
		if d.Name == ToolPresentImage {
			d := d
			def = &d
		}
	}
	require.NotNil(t, def, "present_image is registered")
	assert.Equal(t, PlacementServer, def.Placement)
	assert.Subset(t, def.Tags, []ToolTag{TagFile, TagReadOnly, TagCodingDefault, TagCodingPlan})
	assert.True(t, NeedsMachine(ToolPresentImage))
}
