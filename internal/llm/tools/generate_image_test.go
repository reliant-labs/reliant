// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	openaisdk "github.com/openai/openai-go/v3"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/imagegen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes is a one-pixel PNG. The 0x89 signature byte is the point: it is not
// valid UTF-8, so any path that carries these bytes through a JSON string
// corrupts them. The save_to test below asserts they survive.
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89,
}

// fakeImageGenerator records the request it was handed and returns a canned
// response. It stands in for the driver-layer client so no test issues a
// billed API call.
type fakeImageGenerator struct {
	request  imagegen.Request
	response *imagegen.Response
	err      error
	calls    int
}

func (f *fakeImageGenerator) GenerateImage(ctx context.Context, request imagegen.Request) (*imagegen.Response, error) {
	f.calls++
	f.request = request
	return f.response, f.err
}

func okGenerator() *fakeImageGenerator {
	return &fakeImageGenerator{response: &imagegen.Response{
		Images: []imagegen.Image{{
			Bytes:         pngBytes,
			MIMEType:      "image/png",
			RevisedPrompt: "a red bicycle, studio lighting",
		}},
		ModelID:       "gpt-image-2.5-flare",
		APIModel:      "gpt-image-2.5-flare",
		Driver:        "openai",
		LiteLLMCallID: "call-123",
	}}
}

func resolverFor(generator ImageGenerator) ImageGeneratorResolver {
	return func(ctx context.Context, userID string, selector models.ModelSelector) (ImageGenerator, error) {
		return generator, nil
	}
}

// recordingResolver captures the selector the tool asked for, which is how the
// bound `model` parameter is observed reaching the driver layer.
type recordingResolver struct {
	generator ImageGenerator
	selector  models.ModelSelector
	calls     int
}

func (r *recordingResolver) resolve(ctx context.Context, userID string, selector models.ModelSelector) (ImageGenerator, error) {
	r.calls++
	r.selector = selector
	return r.generator, nil
}

// recordingDaemon captures WriteBinaryFile calls. It embeds daemon.Client so
// the unrelated methods of that interface do not have to be spelled out; any
// of them being called would be a bug and panics loudly.
type recordingDaemon struct {
	daemon.Client
	path    string
	content []byte
	err     error
	// textWrites counts calls that went through the TEXT write path. It must
	// stay zero: WriteFile carries content as a JSON string on the remote
	// path, which replaces every byte that is not valid UTF-8 with U+FFFD and
	// returns no error, so a regression to it is silent at runtime.
	textWrites int
}

func (d *recordingDaemon) WriteFile(ctx context.Context, path string, content string) (*daemon.WriteResult, error) {
	d.textWrites++
	d.path = path
	d.content = []byte(content)
	return &daemon.WriteResult{Created: true, BytesWritten: len(content)}, nil
}

func (d *recordingDaemon) WriteBinaryFile(ctx context.Context, path string, content []byte) (*daemon.WriteResult, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.path = path
	d.content = append([]byte(nil), content...)
	return &daemon.WriteResult{Created: true, BytesWritten: len(content)}, nil
}

// fakeAttachmentRepo records attachment writes in memory. It embeds
// db.Repository so the hundreds of methods this tool never calls do not have to
// be spelled out; any of them being called is a bug and panics loudly rather
// than passing silently.
//
// A fake rather than the package's Postgres harness because that harness
// t.Skip()s when DATABASE_URL is unset, which would make every assertion below
// vanish on a machine with no database instead of failing.
type fakeAttachmentRepo struct {
	db.Repository
	stored map[string]*db.Attachment
	err    error
}

func newFakeAttachmentRepo() *fakeAttachmentRepo {
	return &fakeAttachmentRepo{stored: map[string]*db.Attachment{}}
}

func (r *fakeAttachmentRepo) CreateAttachment(ctx context.Context, att *db.Attachment) error {
	if r.err != nil {
		return r.err
	}
	copied := *att
	r.stored[att.ID] = &copied
	return nil
}

func (r *fakeAttachmentRepo) GetAttachment(ctx context.Context, id string) (*db.Attachment, error) {
	return r.stored[id], nil
}

// generateImageCtx builds a tool context carrying the caller's identity, which
// is how the server executor hands a user id to every server-run tool.
func generateImageCtx(t *testing.T) *rctx.ToolContext {
	t.Helper()
	return createTestContext(t, uuid.New().String())
}

// TestGenerateImage_PersistsAttachmentAndReturnsBinaryParts is the core
// contract: one call must produce BOTH halves of the result. BinaryParts is
// what the model sees (every driver converts it to a native image block), and
// the attachment id is what the user and the UI can reference afterwards.
// Returning only one of the two is the failure this pins.
func TestGenerateImage_PersistsAttachmentAndReturnsBinaryParts(t *testing.T) {
	repo := newFakeAttachmentRepo()

	generator := okGenerator()
	tool := &generateImageTool{repo: repo, resolve: resolverFor(generator)}
	ctx := generateImageCtx(t)

	resp, err := tool.Execute(ctx, GenerateImageParams{
		Prompt:  "a red bicycle",
		Size:    "1024x1024",
		Quality: "high",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)

	// Half one: the model sees the image.
	assert.Equal(t, ToolResponseTypeImage, resp.Type)
	require.Len(t, resp.BinaryParts, 1, "the model must receive the image bytes")
	assert.Equal(t, "image/png", resp.BinaryParts[0].MIMEType)
	assert.Equal(t, pngBytes, resp.BinaryParts[0].Data)

	// Half two: the attachment is persisted and its id is in the text the
	// model reads, so it can cite the image in a later turn.
	var output GenerateImageOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	require.NotEmpty(t, output.AttachmentID)
	assert.Contains(t, resp.Content, output.AttachmentID,
		"the attachment id must appear in the tool text, not only in metadata")

	att, err := repo.GetAttachment(ctx.Context, output.AttachmentID)
	require.NoError(t, err)
	require.NotNil(t, att)
	assert.Equal(t, pngBytes, att.Content, "bytes must be stored in the database, not on a filesystem")
	assert.Equal(t, "image/png", att.MimeType)
	assert.Equal(t, int64(len(pngBytes)), att.Size)
	assert.Equal(t, "test-user", att.UserID)

	// The attachment type is what makes this render as an image block rather
	// than erroring out in save_message's type switch.
	assert.Equal(t, string(attachment.TypeImage), att.AttachmentType)
	assert.Equal(t, attachment.TypeImage, attachment.GetAttachmentType(att.Filename),
		"the filename extension must classify as an image; save_message rejects unknown types")

	// Params reach the driver untranslated, and the tool never names a model.
	assert.Equal(t, "a red bicycle", generator.request.Prompt)
	assert.Equal(t, "1024x1024", generator.request.Size)
	assert.Equal(t, "high", generator.request.Quality)
	assert.Equal(t, 1, generator.request.Count)
}

// TestGenerateImage_SaveToWritesBinaryToDaemon pins that save_to goes through
// WriteBinaryFile. WriteFile carries content as a JSON string on the remote
// path, where the 0x89 PNG signature becomes U+FFFD with no error returned —
// so asserting the exact bytes is what catches a regression to WriteFile.
func TestGenerateImage_SaveToWritesBinaryToDaemon(t *testing.T) {
	repo := newFakeAttachmentRepo()

	tool := &generateImageTool{repo: repo, resolve: resolverFor(okGenerator())}
	ctx := generateImageCtx(t)
	ctx.Project = &db.Project{Path: "/workspace"}
	recorder := &recordingDaemon{}
	ctx = ctx.WithDaemon(recorder)

	resp, err := tool.Execute(ctx, GenerateImageParams{
		Prompt: "a red bicycle",
		SaveTo: "assets/bike.png",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)

	assert.Equal(t, 0, recorder.textWrites,
		"image bytes must go through WriteBinaryFile; the text write path corrupts them silently")
	assert.Equal(t, filepath.Join("/workspace", "assets/bike.png"), recorder.path,
		"a relative save_to must resolve against the working directory")
	assert.Equal(t, pngBytes, recorder.content, "binary must survive the write path unchanged")

	var output GenerateImageOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	assert.Equal(t, recorder.path, output.SavedTo)
	assert.Contains(t, resp.Content, recorder.path, "the resolved path must be reported to the model")
}

// TestGenerateImage_SaveToFailureStillReturnsTheImage covers the asymmetry
// that matters: the generation already cost money and the attachment is
// already durable, so a failed file write must degrade to a warning rather
// than throwing the image away.
func TestGenerateImage_SaveToFailureStillReturnsTheImage(t *testing.T) {
	repo := newFakeAttachmentRepo()

	tool := &generateImageTool{repo: repo, resolve: resolverFor(okGenerator())}

	t.Run("no daemon connected", func(t *testing.T) {
		ctx := generateImageCtx(t)
		ctx.Project = &db.Project{Path: "/workspace"}

		resp, err := tool.Execute(ctx, GenerateImageParams{Prompt: "x", SaveTo: "out.png"})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "a failed save must not discard a generated image")
		require.Len(t, resp.BinaryParts, 1)
		assert.Contains(t, resp.Content, "WARNING")

		var output GenerateImageOutput
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
		assert.NotEmpty(t, output.AttachmentID)
		assert.Empty(t, output.SavedTo)
	})

	t.Run("daemon rejects the write", func(t *testing.T) {
		ctx := generateImageCtx(t)
		ctx.Project = &db.Project{Path: "/workspace"}
		ctx = ctx.WithDaemon(&recordingDaemon{err: fmt.Errorf("permission denied")})

		resp, err := tool.Execute(ctx, GenerateImageParams{Prompt: "x", SaveTo: "out.png"})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		require.Len(t, resp.BinaryParts, 1)
		assert.Contains(t, resp.Content, "permission denied")
	})
}

// TestGenerateImage_Guards checks that every unavailable dependency produces a
// readable tool error rather than a panic or a nil dereference.
func TestGenerateImage_Guards(t *testing.T) {
	repo := newFakeAttachmentRepo()

	t.Run("empty prompt", func(t *testing.T) {
		tool := &generateImageTool{repo: repo, resolve: resolverFor(okGenerator())}
		resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "   "})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "prompt is required")
	})

	t.Run("no resolver wired", func(t *testing.T) {
		tool := &generateImageTool{repo: repo}
		resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "x"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "not available")
	})

	t.Run("no repository", func(t *testing.T) {
		tool := &generateImageTool{resolve: resolverFor(okGenerator())}
		resp, err := tool.Execute(createTestContext(t, ""), GenerateImageParams{Prompt: "x"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "database")
	})

	t.Run("generation fails", func(t *testing.T) {
		failing := &fakeImageGenerator{err: fmt.Errorf("insufficient credits")}
		tool := &generateImageTool{repo: repo, resolve: resolverFor(failing)}
		resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "x"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "insufficient credits")
	})

	t.Run("provider returns no images", func(t *testing.T) {
		empty := &fakeImageGenerator{response: &imagegen.Response{}}
		tool := &generateImageTool{repo: repo, resolve: resolverFor(empty)}
		resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "x"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "no images")
	})
}

// TestGenerateImage_FilenameFollowsSniffedMIMEType pins the coupling between
// the MIME type imagegen sniffs from the bytes and the extension the file gets.
// They must agree, because attachment.GetAttachmentType classifies by
// extension and save_message errors on an attachment type it does not know.
func TestGenerateImage_FilenameFollowsSniffedMIMEType(t *testing.T) {
	t.Parallel()
	for mimeType, wantExt := range map[string]string{
		"image/png":  ".png",
		"image/jpeg": ".jpg",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"":           ".png",
	} {
		filename := generatedImageFilename(mimeType)
		assert.Equal(t, wantExt, filepath.Ext(filename), "mime %q", mimeType)
		assert.Equal(t, attachment.TypeImage, attachment.GetAttachmentType(filename),
			"every generated filename must classify as an image attachment")
	}
}

// TestGenerateImage_Registration pins the registry facts that decide who gets
// this tool: opt-in via tag:media, never in tag:default, and server-located.
func TestGenerateImage_Registration(t *testing.T) {
	t.Parallel()

	var found *ToolDefinition
	for i, def := range GetToolRegistry() {
		if def.Name == ToolGenerateImage {
			found = &GetToolRegistry()[i]
			break
		}
	}
	require.NotNil(t, found, "generate_image must be registered")
	assert.Equal(t, ToolRunsOnServer, found.RunsOn,
		"generation needs the database and network, which the daemon does not have")
	assert.Contains(t, found.Tags, TagMedia)
	assert.NotContains(t, found.Tags, TagDefault,
		"generate_image costs money and must not appear in every coding workflow")

	assert.Contains(t, ExpandToolFilter([]string{"tag:media"}, nil), ToolGenerateImage)
	assert.NotContains(t, ExpandToolFilter([]string{"tag:default"}, nil), ToolGenerateImage)
}

// TestGenerateImage_ParamSchemaOffersNoModelChoice pins the deliberate absence
// of a model parameter from the AGENT's view. The parameter now exists — a
// human can bind it — but it is bound by default, so the schema the model sees
// is byte-for-byte what it was when there was no model parameter at all.
func TestGenerateImage_ParamSchemaOffersNoModelChoice(t *testing.T) {
	t.Parallel()
	schema := NewGenerateImageTool(nil, nil).ParamSchema()
	require.NotNil(t, schema.Properties)

	var names []string
	for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
		names = append(names, pair.Key)
	}
	assert.Contains(t, names, "prompt")
	assert.Contains(t, names, "save_to")
	assert.NotContains(t, names, "model", "the agent must not name a model id")
	assert.Equal(t, []string{"prompt"}, schema.Required)
}

// TestGenerateImage_DefaultModelBindingIsAStrategy pins that the unconfigured
// model default is a TAG, never a concrete model id. A pinned id keeps being
// requested after the model is retired; a tag re-resolves against the registry
// on every call. We have shipped retired image models by pinning.
func TestGenerateImage_DefaultModelBindingIsAStrategy(t *testing.T) {
	t.Parallel()

	tool := NewGenerateImageTool(nil, nil)
	bindable, ok := tool.(BindableTool)
	require.True(t, ok, "generate_image must be bindable")

	bound, present := bindable.Bindings()["model"]
	require.True(t, present, "model must be bound by default so the agent never sees it")

	selector, ok := bound.Literal.(models.ModelSelector)
	require.True(t, ok, "the model binding must be a ModelSelector, got %T", bound.Literal)
	assert.Equal(t, []string{ImageGenTag}, selector.Tags)
	assert.Empty(t, selector.ID, "the default must be a tag strategy, never a pinned model id")
	assert.Empty(t, selector.Providers)
}

// TestGenerateImage_UnboundModelResolvesByTag is the zero-configuration
// invariant: with nothing bound by a human, the selector that reaches the
// driver layer is exactly the tags:[image-gen] request the hardcoded
// implementation used to make.
func TestGenerateImage_UnboundModelResolvesByTag(t *testing.T) {
	repo := newFakeAttachmentRepo()
	recorder := &recordingResolver{generator: okGenerator()}
	tool := NewGenerateImageTool(repo, recorder.resolve)

	resp, err := tool.Run(generateImageCtx(t), ToolCall{ID: "c1", Input: `{"prompt":"a red bicycle"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)

	require.Equal(t, 1, recorder.calls)
	assert.Equal(t, models.ModelSelector{Tags: []string{ImageGenTag}}, recorder.selector)
}

// TestGenerateImage_ModelBindingReachesTheDriver is the point of the whole
// exercise: a human can now fix the image model without the agent gaining any
// say in it.
func TestGenerateImage_ModelBindingReachesTheDriver(t *testing.T) {
	repo := newFakeAttachmentRepo()
	recorder := &recordingResolver{generator: okGenerator()}

	configured, err := BindTool(NewGenerateImageTool(repo, recorder.resolve), Bindings{
		"model": LiteralBinding(models.ModelSelector{
			Tags:      []string{ImageGenTag},
			Providers: []string{"codex"},
		}),
	})
	require.NoError(t, err)

	// Still invisible to the agent after rebinding.
	names := schemaPropertyNames(t, configured)
	assert.NotContains(t, names, "model")
	assert.Contains(t, names, "prompt")

	// Even if the model names one anyway, the human's binding wins.
	resp, err := configured.Run(generateImageCtx(t), ToolCall{
		ID:    "c1",
		Input: `{"prompt":"a red bicycle","model":{"id":"some-model-the-agent-guessed"}}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)

	require.Equal(t, 1, recorder.calls)
	assert.Equal(t, models.ModelSelector{
		Tags:      []string{ImageGenTag},
		Providers: []string{"codex"},
	}, recorder.selector, "the bound selector must win over an agent-supplied model")
}

// TestGenerateImage_EndToEndThroughRealClient runs the tool against a real
// imagegen.Client pointed at an httptest server, so the wire decoding, the
// MIME sniff and the attachment write are all exercised together. No network
// leaves the process and nothing is billed.
func TestGenerateImage_EndToEndThroughRealClient(t *testing.T) {
	repo := newFakeAttachmentRepo()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-litellm-call-id", "call-abc")
		_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q,"revised_prompt":"a tidy red bicycle"}]}`,
			base64.StdEncoding.EncodeToString(pngBytes))
	}))
	defer server.Close()

	// openaisdk.NewClient rather than llm.NewOpenAISDKClient: this package
	// cannot import internal/llm (that edge is a cycle), and the idle-timeout
	// wrapper the sanctioned constructor installs is covered by internal/llm's
	// own tests. What matters here is that the tool drives a real client.
	client, err := imagegen.New(imagegen.Config{
		BaseURL:  server.URL,
		APIKey:   "test-key",
		ModelID:  "gpt-image-2.5-flare",
		APIModel: "gpt-image-2.5-flare",
		Driver:   "openai",
	}, openaisdk.NewClient)
	require.NoError(t, err)

	tool := &generateImageTool{repo: repo, resolve: resolverFor(client)}
	ctx := generateImageCtx(t)

	resp, err := tool.Execute(ctx, GenerateImageParams{Prompt: "a red bicycle", Background: "transparent"})
	require.NoError(t, err)
	require.False(t, resp.IsError, "unexpected error response: %s", resp.Content)

	assert.Equal(t, "a red bicycle", gotBody["prompt"])
	assert.Equal(t, "transparent", gotBody["background"])
	assert.NotContains(t, gotBody, "quality", "an omitted param must not be sent as an empty string")

	require.Len(t, resp.BinaryParts, 1)
	assert.Equal(t, pngBytes, resp.BinaryParts[0].Data)
	assert.Equal(t, "image/png", resp.BinaryParts[0].MIMEType,
		"the MIME type must be sniffed from the bytes, not assumed")
	assert.True(t, strings.Contains(resp.Content, "tidy red bicycle"),
		"the provider's revised prompt is the clearest signal of how the prompt was read")

	var output GenerateImageOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	att, err := repo.GetAttachment(ctx.Context, output.AttachmentID)
	require.NoError(t, err)
	require.NotNil(t, att)
	assert.Equal(t, pngBytes, att.Content)
}
