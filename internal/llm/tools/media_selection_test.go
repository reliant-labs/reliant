// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/drivers/videogen"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// selectorRecorder captures every selector a media tool resolves, and answers
// from a table so a test can decide which tiers "the user's providers" serve.
type selectorRecorder struct {
	seen    []models.ModelSelector
	servers map[string]string // "tag,tag" -> model id; absent means unavailable
}

func (r *selectorRecorder) key(selector models.ModelSelector) string {
	if selector.ID != "" {
		return "id:" + selector.ID
	}
	return strings.Join(selector.Tags, ",")
}

func (r *selectorRecorder) modelFor(selector models.ModelSelector) (string, error) {
	r.seen = append(r.seen, selector)
	if r.servers == nil {
		return "veo-3.1-generate", nil
	}
	id, ok := r.servers[r.key(selector)]
	if !ok {
		return "", fmt.Errorf("no model for %v", selector.Tags)
	}
	return id, nil
}

type namedVideoGenerator struct {
	fakeVideoGenerator
	modelID string
}

func (g *namedVideoGenerator) Info() videogen.ModelInfo {
	info := g.fakeVideoGenerator.Info()
	info.ModelID = g.modelID
	return info
}

func (r *selectorRecorder) videoResolver(caps map[string]*models.VideoCapabilities) VideoGeneratorResolver {
	return func(_ context.Context, _ string, selector models.ModelSelector) (VideoGenerator, error) {
		id, err := r.modelFor(selector)
		if err != nil {
			return nil, err
		}
		return &namedVideoGenerator{fakeVideoGenerator: fakeVideoGenerator{caps: caps[id]}, modelID: id}, nil
	}
}

func omniCapsNo4K() *models.VideoCapabilities {
	return &models.VideoCapabilities{
		Durations: []int{3, 4, 5, 6, 7, 8, 9, 10}, Resolutions: []string{"360p", "720p", "1080p"},
		Aspects: []string{"16:9", "9:16"}, MaxReferenceImages: 3, SupportsEdit: true, SupportsExtend: true,
	}
}

func veo4KCaps() *models.VideoCapabilities {
	caps := veoCaps()
	caps.Resolutions = []string{"720p", "1080p", "4k"}
	caps.HighResolutions = []string{"1080p", "4k"}
	return caps
}

func videoToolWith(r *selectorRecorder, caps map[string]*models.VideoCapabilities) *generateVideoTool {
	tool := newVideoTool(newFakeAttachmentRepo(), newFakeVideoJobs(), &fakeVideoGenerator{caps: veoCaps()})
	tool.resolve = r.videoResolver(caps)
	return tool
}

func TestMediaTiers_QualityMapsToTags(t *testing.T) {
	for _, c := range []struct {
		quality string
		want    []string
	}{
		{"", []string{"video-gen", "flagship"}},
		{"standard", []string{"video-gen", "flagship"}},
		{"fast", []string{"video-gen", "cheap"}},
		{"cinematic", []string{"video-gen", "cinematic"}},
	} {
		t.Run("video/"+c.quality, func(t *testing.T) {
			choice, err := chooseMedia(videoKind, models.ModelSelector{}, c.quality)
			require.NoError(t, err)
			assert.Equal(t, c.want, choice.Selector.Tags)
			assert.Empty(t, choice.Selector.ID, "a tier is a strategy, never a pinned id")
		})
	}
	for _, c := range []struct {
		tier string
		want []string
	}{
		{"", []string{"image-gen", "flagship"}},
		{"standard", []string{"image-gen", "flagship"}},
		{"fast", []string{"image-gen", "fast"}},
	} {
		t.Run("image/"+c.tier, func(t *testing.T) {
			choice, err := chooseMedia(imageKind, models.ModelSelector{}, c.tier)
			require.NoError(t, err)
			assert.Equal(t, c.want, choice.Selector.Tags)
		})
	}

	_, err := chooseMedia(videoKind, models.ModelSelector{}, "ultra")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fast, standard, cinematic")
}

func TestGenerateVideo_QualityReachesResolverAsTags(t *testing.T) {
	recorder := &selectorRecorder{}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{"veo-3.1-generate": veoCaps()})

	resp, err := tool.Execute(videoCtx(t, "tc-q1"), GenerateVideoParams{Prompt: "x", Quality: "cinematic"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.NotEmpty(t, recorder.seen)
	assert.Equal(t, []string{"video-gen", "cinematic"}, recorder.seen[0].Tags)
}

func TestGenerateVideo_ExplicitModelBeatsQuality(t *testing.T) {
	recorder := &selectorRecorder{}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{"veo-3.1-generate": veoCaps()})

	resp, err := tool.Execute(videoCtx(t, "tc-q2"), GenerateVideoParams{
		Prompt: "x", Quality: "fast", Model: models.ModelSelector{ID: "veo-3.1-generate"},
	})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, models.ModelSelector{ID: "veo-3.1-generate"}, recorder.seen[0], "an explicit model wins over quality")
	assert.Contains(t, resp.Content, "Chosen: veo-3.1-generate via model=veo-3.1-generate")
}

func TestGenerateVideo_InvalidExplicitModelListsValidOnes(t *testing.T) {
	recorder := &selectorRecorder{}
	tool := videoToolWith(recorder, nil)

	for _, bad := range []string{"claude-5.5-opus", "sora-2", "veo-9"} {
		resp, err := tool.Execute(videoCtx(t, "tc-q3-"+bad), GenerateVideoParams{Prompt: "x", Model: models.ModelSelector{ID: bad}})
		require.NoError(t, err)
		require.True(t, resp.IsError, bad)
		assert.Contains(t, resp.Content, fmt.Sprintf("%q is not a video model", bad))
		for _, valid := range []string{"gemini-omni-1.1-flash", "veo-3.1-generate", "veo-3.1-fast-generate", "veo-3.1-lite-generate"} {
			assert.Contains(t, resp.Content, valid)
		}
		assert.NotContains(t, resp.Content, "gpt-image", "image models are not valid video models")
	}
	assert.Empty(t, recorder.seen, "an invalid id must be refused before any provider call")
}

func TestGenerateVideo_BoundModelLocksAndIsHiddenFromSchema(t *testing.T) {
	recorder := &selectorRecorder{servers: map[string]string{"id:veo-3.1-lite-generate": "veo-3.1-lite-generate"}}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{"veo-3.1-lite-generate": veoCaps()})

	open := schemaPropertyNames(t, NewToolWrapper[GenerateVideoParams, ToolResponse](tool))
	assert.Contains(t, open, "model", "model is OPEN by default")
	assert.Contains(t, open, "quality")

	locked, err := BindTool(NewToolWrapper[GenerateVideoParams, ToolResponse](tool), Bindings{
		"model": LiteralBinding(models.ModelSelector{ID: "veo-3.1-lite-generate"}),
	})
	require.NoError(t, err)
	names := schemaPropertyNames(t, locked)
	assert.NotContains(t, names, "model", "a bound param is removed from the LLM schema")
	assert.Contains(t, names, "quality")

	refused, err := locked.Run(videoCtx(t, "tc-q4"), ToolCall{ID: "c", Input: `{"prompt":"x","quality":"cinematic","model":"veo-3.1-generate"}`})
	require.NoError(t, err)
	assert.True(t, refused.IsError, "agent input for the bound model is refused, not honored")
	assert.Empty(t, recorder.seen)

	resp, err := locked.Run(videoCtx(t, "tc-q5"), ToolCall{ID: "c2", Input: `{"prompt":"x","quality":"cinematic"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, models.ModelSelector{ID: "veo-3.1-lite-generate"}, recorder.seen[0], "the human's binding is what runs")

	qualityLocked, err := BindTool(NewToolWrapper[GenerateVideoParams, ToolResponse](tool), Bindings{"quality": LiteralBinding("fast")})
	require.NoError(t, err)
	assert.NotContains(t, schemaPropertyNames(t, qualityLocked), "quality")
}

func TestGenerateVideo_ModelParamSchemaListsValidIDs(t *testing.T) {
	schema := newVideoTool(newFakeAttachmentRepo(), newFakeVideoJobs(), &fakeVideoGenerator{caps: veoCaps()})
	prop, ok := NewToolWrapper[GenerateVideoParams, ToolResponse](schema).ParamSchema().Properties.Get("model")
	require.True(t, ok)
	assert.Equal(t, "string", prop.Type, "the agent names a model by id")
	assert.Contains(t, prop.Description, "veo-3.1-generate")
	assert.Contains(t, prop.Description, "gemini-omni-1.1-flash")
}

func TestGenerateVideo_4KOnOmniErrorsAndNamesCinematic(t *testing.T) {
	recorder := &selectorRecorder{servers: map[string]string{
		"video-gen,flagship":  "gemini-omni-1.1-flash",
		"video-gen,cinematic": "veo-3.1-generate",
		"video-gen,cheap":     "veo-3.1-lite-generate",
	}}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{
		"gemini-omni-1.1-flash": omniCapsNo4K(), "veo-3.1-generate": veo4KCaps(),
	})

	resp, err := tool.Execute(videoCtx(t, "tc-q5"), GenerateVideoParams{Prompt: "x", Resolution: "4k"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "gemini-omni-1.1-flash")
	assert.Contains(t, resp.Content, `resolution="4k"`)
	assert.Contains(t, resp.Content, "Tiers that support this: cinematic")
	assert.Contains(t, resp.Content, "quality=cinematic")
	assert.NotContains(t, resp.Content, "fast", "lite has no 4k, so fast is not offered")
}

func TestGenerateVideo_ResultNamesModelTierAndReason(t *testing.T) {
	recorder := &selectorRecorder{servers: map[string]string{"video-gen,cinematic": "veo-3.1-generate"}}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{"veo-3.1-generate": veoCaps()})

	resp, err := tool.Execute(videoCtx(t, "tc-q6"), GenerateVideoParams{Prompt: "x", Quality: "cinematic"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Chosen: veo-3.1-generate via quality=cinematic.")

	var output GenerateVideoOutput
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
	assert.Equal(t, "Chosen: veo-3.1-generate via quality=cinematic.", output.Chosen)

	recorder2 := &selectorRecorder{servers: map[string]string{"video-gen,flagship": "gemini-omni-1.1-flash"}}
	tool2 := videoToolWith(recorder2, map[string]*models.VideoCapabilities{"gemini-omni-1.1-flash": omniCapsNo4K()})
	resp2, err := tool2.Execute(videoCtx(t, "tc-q6b"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	assert.Contains(t, resp2.Content, "Chosen: gemini-omni-1.1-flash via default quality=standard.")
}

func TestGenerateVideo_TierNotServedNamesAvailableAlternatives(t *testing.T) {
	// A user whose only video provider carries Omni: cinematic degrades onto
	// the standard model by tag scoring, which must be refused, not passed off.
	recorder := &selectorRecorder{servers: map[string]string{
		"video-gen,flagship":  "gemini-omni-1.1-flash",
		"video-gen,cinematic": "gemini-omni-1.1-flash",
		"video-gen,cheap":     "gemini-omni-1.1-flash",
	}}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{"gemini-omni-1.1-flash": omniCapsNo4K()})

	resp, err := tool.Execute(videoCtx(t, "tc-q7"), GenerateVideoParams{Prompt: "x", Quality: "cinematic"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "quality=cinematic isn't available with your providers")
	assert.Contains(t, resp.Content, "standard is")
}

func TestGenerateImage_TierMapsToTagsAndResultNamesModel(t *testing.T) {
	repo := newFakeAttachmentRepo()
	recorder := &recordingResolver{generator: okGenerator()}
	tool := NewGenerateImageTool(repo, recorder.resolve)

	resp, err := tool.Run(generateImageCtx(t), ToolCall{ID: "c1", Input: `{"prompt":"a red bicycle","tier":"fast"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, models.ModelSelector{Tags: []string{ImageGenTag, models.TagFast}}, recorder.selector)
	assert.Contains(t, resp.Content, "Chosen: gpt-image-2.5-flare via tier=fast.")
}

func TestGenerateImage_ExplicitModelBeatsTierAndInvalidListsValid(t *testing.T) {
	repo := newFakeAttachmentRepo()
	recorder := &recordingResolver{generator: okGenerator()}
	tool := NewGenerateImageTool(repo, recorder.resolve)

	resp, err := tool.Run(generateImageCtx(t), ToolCall{ID: "c1", Input: `{"prompt":"x","tier":"fast","model":"gpt-image-2"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Equal(t, models.ModelSelector{ID: "gpt-image-2"}, recorder.selector)

	recorder.calls = 0
	resp, err = tool.Run(generateImageCtx(t), ToolCall{ID: "c2", Input: `{"prompt":"x","model":"veo-3.1-generate"}`})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "is not a image model")
	for _, valid := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gemini-3.1-flash-image"} {
		assert.Contains(t, resp.Content, valid)
	}
	assert.NotContains(t, resp.Content, "veo-3.1-lite", "video models are not valid image models")
	assert.Equal(t, 0, recorder.calls)
}

func TestGenerateImage_TierNotServedNamesAvailableAlternatives(t *testing.T) {
	repo := newFakeAttachmentRepo()
	resolve := func(_ context.Context, _ string, selector models.ModelSelector) (ImageGenerator, error) {
		if len(selector.Tags) == 2 && selector.Tags[1] == models.TagFast {
			return &namedImageGenerator{fakeImageGenerator: *okGenerator(), model: "gpt-image-2"}, nil // degraded onto a non-fast model
		}
		return &namedImageGenerator{fakeImageGenerator: *okGenerator(), model: "gpt-image-2.5-sunburst"}, nil
	}
	tool := NewGenerateImageTool(repo, resolve)

	resp, err := tool.Run(generateImageCtx(t), ToolCall{ID: "c1", Input: `{"prompt":"x","tier":"fast"}`})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "tier=fast isn't available with your providers")
	assert.Contains(t, resp.Content, "standard is")
}

type namedImageGenerator struct {
	fakeImageGenerator
	model string
}

func (g *namedImageGenerator) ModelID() string { return g.model }

func TestGenerateImage_BoundModelLocksAndTierStaysOpen(t *testing.T) {
	names := schemaPropertyNames(t, NewGenerateImageTool(nil, nil))
	assert.Contains(t, names, "model")
	assert.Contains(t, names, "tier")

	locked, err := BindTool(NewGenerateImageTool(nil, nil), Bindings{"model": LiteralBinding(models.ModelSelector{ID: "gpt-image-2"})})
	require.NoError(t, err)
	assert.NotContains(t, schemaPropertyNames(t, locked), "model")
	assert.Contains(t, schemaPropertyNames(t, locked), "tier")
}

// withAvailability installs a media availability check for one test.
func withAvailability(t *testing.T, check MediaAvailabilityCheck) {
	t.Helper()
	SetMediaAvailabilityCheck(check)
	t.Cleanup(func() { SetMediaAvailabilityCheck(nil) })
}

func noProviderFor(modalities ...models.Modality) MediaAvailabilityCheck {
	return func(_ context.Context, _ string, modality models.Modality) error {
		for _, m := range modalities {
			if m == modality {
				return models.NewMediaUnavailableError(models.MustGetRegistry(), modality)
			}
		}
		return nil
	}
}

func TestLoadTool_RefusesVideoWithTheFixWhenNoProvider(t *testing.T) {
	withAvailability(t, noProviderFor(models.ModalityVideo))
	tc, _ := scopeWithAccess(t, nil, []string{LoadableWildcard})
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "test-user")
	tool := &loadToolTool{}

	resp, err := tool.Execute(tc, LoadToolParams{Name: ToolGenerateVideo})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "Video generation needs a Gemini API key or Reliant-managed credits — add one in Settings → AI providers")
	assert.Empty(t, grantsOf(t, resp), "an unusable media tool must not be granted")

	resp, err = tool.Execute(tc, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "image is still available: %s", resp.Content)
	assert.Equal(t, []string{ToolGenerateImage}, grantsOf(t, resp))
}

func TestLoadTool_RefusesImageWithTheFixWhenNoProvider(t *testing.T) {
	withAvailability(t, noProviderFor(models.ModalityImage))
	tc, _ := scopeWithAccess(t, nil, []string{LoadableWildcard})
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "test-user")

	resp, err := (&loadToolTool{}).Execute(tc, LoadToolParams{Name: ToolGenerateImage})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "Image generation needs ")
	assert.Contains(t, resp.Content, "an OpenAI API key")
	assert.Contains(t, resp.Content, "a Gemini API key")
	assert.Contains(t, resp.Content, "add one in Settings → AI providers")
	assert.Empty(t, grantsOf(t, resp))
}

func TestLoadTool_TagLoadSkipsUnavailableMediaTool(t *testing.T) {
	withAvailability(t, noProviderFor(models.ModalityVideo))
	tc, _ := scopeWithAccess(t, nil, []string{LoadableWildcard})
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "test-user")

	resp, err := (&loadToolTool{}).Execute(tc, LoadToolParams{Name: "tag:media"})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "Video generation needs a Gemini API key")
	assert.NotContains(t, grantsOf(t, resp), ToolGenerateVideo)
	assert.Contains(t, grantsOf(t, resp), ToolGenerateImage)
}

func TestLoadTool_SearchStillListsUnavailableMediaToolAnnotated(t *testing.T) {
	withAvailability(t, noProviderFor(models.ModalityVideo))
	tc, _ := scopeWithAccess(t, nil, []string{LoadableWildcard})
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "test-user")

	resp, err := (&loadToolTool{}).Execute(tc, LoadToolParams{Query: "generate_video"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "**generate_video**")
	assert.Contains(t, resp.Content, "unavailable: no video-capable provider configured")
	assert.Contains(t, resp.Content, "Gemini API key")

	resp, err = (&loadToolTool{}).Execute(tc, LoadToolParams{Query: "generate_image"})
	require.NoError(t, err)
	assert.NotContains(t, resp.Content, "unavailable", "image has a provider")
}

func TestMediaUnavailableMessage_IdenticalAtLoadToolAndCallTime(t *testing.T) {
	withAvailability(t, noProviderFor(models.ModalityVideo))
	tc, _ := scopeWithAccess(t, nil, []string{LoadableWildcard})
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "test-user")

	shared := models.MediaUnavailableMessage(models.MustGetRegistry(), models.ModalityVideo)

	loadResp, err := (&loadToolTool{}).Execute(tc, LoadToolParams{Name: ToolGenerateVideo})
	require.NoError(t, err)
	assert.Contains(t, loadResp.Content, shared)

	resolver := func(context.Context, string, models.ModelSelector) (VideoGenerator, error) {
		return nil, models.NewMediaUnavailableError(models.MustGetRegistry(), models.ModalityVideo)
	}
	tool := newVideoTool(newFakeAttachmentRepo(), newFakeVideoJobs(), nil)
	tool.resolve = resolver
	callResp, err := tool.Execute(videoCtx(t, "tc-shared"), GenerateVideoParams{Prompt: "x"})
	require.NoError(t, err)
	require.True(t, callResp.IsError)
	assert.Equal(t, shared, callResp.Content, "the call-time backstop shows exactly the shared message")
}

func TestLoadTool_NoCheckInstalledDoesNotBlock(t *testing.T) {
	SetMediaAvailabilityCheck(nil)
	tc, _ := scopeWithAccess(t, nil, []string{LoadableWildcard})
	tc.Context = context.WithValue(tc.Context, auth.UserIDContextKey, "test-user")

	resp, err := (&loadToolTool{}).Execute(tc, LoadToolParams{Name: ToolGenerateVideo})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Equal(t, []string{ToolGenerateVideo}, grantsOf(t, resp))
}

// Omni accepts 4K but renders at 720p and upscales. The request is valid and
// runs, but the agent must be TOLD — otherwise it reports "4K video" to a user
// who wanted sharp 4K and should have been offered quality=cinematic.
func TestGenerateVideo_UpscaledResolutionIsDisclosed(t *testing.T) {
	omni := &models.VideoCapabilities{
		Durations:         []int{3, 4, 5, 6, 7, 8, 9, 10},
		Resolutions:       []string{"360p", "720p", "1080p", "4k"},
		NativeResolutions: []string{"360p", "720p"},
		Aspects:           []string{"16:9", "9:16"},
	}
	recorder := &selectorRecorder{servers: map[string]string{"video-gen,flagship": "gemini-omni-1.1-flash"}}
	tool := videoToolWith(recorder, map[string]*models.VideoCapabilities{"gemini-omni-1.1-flash": omni})

	resp, err := tool.Execute(videoCtx(t, "tc-upscale"), GenerateVideoParams{Prompt: "x", Resolution: "4k"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "4k is upscaled")
	assert.Contains(t, resp.Content, "quality=cinematic")

	native, err := tool.Execute(videoCtx(t, "tc-native"), GenerateVideoParams{Prompt: "x", Resolution: "720p"})
	require.NoError(t, err)
	assert.NotContains(t, native.Content, "upscaled", "a native resolution needs no note")
}
