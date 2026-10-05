// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/modelprefs"
)

func TestMediaUnavailableMessage_DerivedFromCatalog(t *testing.T) {
	registry := models.MustGetRegistry()

	video := models.MediaUnavailableMessage(registry, models.ModalityVideo)
	assert.Equal(t, "Video generation needs a Gemini API key or Reliant-managed credits — add one in Settings → AI providers", video)

	image := models.MediaUnavailableMessage(registry, models.ModalityImage)
	assert.Contains(t, image, "Image generation needs ")
	for _, label := range []string{"a Gemini API key", "an OpenAI API key", "a ChatGPT (Codex) login"} {
		assert.Contains(t, image, label)
	}
	assert.Contains(t, image, "add one in Settings → AI providers")
}

func TestCheckMediaAvailable_NoProviderYieldsSharedError(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)
	ctx := context.Background()

	// Only an anthropic key: serves neither image nor video.
	require.NoError(t, repo.SetProviderAPIKey(ctx, "media-none", "anthropic", "sk-ant-test"))

	for _, modality := range []models.Modality{models.ModalityVideo, models.ModalityImage} {
		err := CheckMediaAvailable(ctx, "media-none", modality)
		var unavailable *models.MediaUnavailableError
		require.True(t, errors.As(err, &unavailable), "%s: want MediaUnavailableError, got %v", modality, err)
		assert.Equal(t, models.MediaUnavailableMessage(models.MustGetRegistry(), modality), unavailable.Message)
	}

	// The resolvers (the call-time backstop) return the same message.
	_, err := ResolveVideoGenerator(ctx, "media-none", models.ModelSelector{Tags: []string{"video-gen", "flagship"}})
	var unavailable *models.MediaUnavailableError
	require.True(t, errors.As(err, &unavailable))
	assert.Equal(t, models.MediaUnavailableMessage(models.MustGetRegistry(), models.ModalityVideo), err.Error())

	_, err = ResolveImageGenerator(ctx, "media-none", models.ModelSelector{Tags: []string{"image-gen", "flagship"}})
	require.True(t, errors.As(err, &unavailable))
	assert.Equal(t, models.MediaUnavailableMessage(models.MustGetRegistry(), models.ModalityImage), err.Error())
}

func TestCheckMediaAvailable_ProvidersThatServeTheModality(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)
	ctx := context.Background()

	require.NoError(t, repo.SetProviderAPIKey(ctx, "media-gem", "gemini", "AIza-test"))
	assert.NoError(t, CheckMediaAvailable(ctx, "media-gem", models.ModalityVideo))
	assert.NoError(t, CheckMediaAvailable(ctx, "media-gem", models.ModalityImage))

	// Managed credit serves both image and video.
	require.NoError(t, repo.SetProviderAPIKey(ctx, "media-managed", "reliant", "rlat_abcdef0123456789abcdef0123456789"))
	assert.NoError(t, CheckMediaAvailable(ctx, "media-managed", models.ModalityVideo))
	assert.NoError(t, CheckMediaAvailable(ctx, "media-managed", models.ModalityImage))
}

func TestResolveVideo_TierSelectsModelAndPreferenceOnlyAppliesToDefault(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)
	ctx := context.Background()
	userID := "media-prefs"
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "gemini", "AIza-test"))

	resolve := func(tags ...string) string {
		resolved, _, err := resolveMediaModel(ctx, userID, models.ModelSelector{Tags: tags}, models.ModalityVideo)
		require.NoError(t, err)
		return resolved.Definition.ID
	}

	assert.Equal(t, "gemini-omni-1.1-flash", resolve("video-gen", "flagship"), "standard -> Omni")
	assert.Equal(t, "veo-3.1-generate", resolve("video-gen", "cinematic"), "cinematic -> Veo 3.1")
	assert.Equal(t, "veo-3.1-lite-generate", resolve("video-gen", "cheap"), "fast -> Veo Lite")

	// The user's Settings preference for the video-gen tier applies to the
	// default (standard) selector, via the same row the chat tiers use.
	require.NoError(t, setTagPref(ctx, repo, userID, "video-gen", `{"model_id":"veo-3.1-fast-generate"}`))
	assert.Equal(t, "veo-3.1-fast-generate", resolve("video-gen", "flagship"), "preference beats the default")

	// ...but never an explicit tier or model: those are specific requests.
	assert.Equal(t, "veo-3.1-generate", resolve("video-gen", "cinematic"))
	assert.Equal(t, "veo-3.1-lite-generate", resolve("video-gen", "cheap"))
	explicit, _, err := resolveMediaModel(ctx, userID, models.ModelSelector{ID: "gemini-omni-1.1-flash"}, models.ModalityVideo)
	require.NoError(t, err)
	assert.Equal(t, "gemini-omni-1.1-flash", explicit.Definition.ID, "explicit model beats the preference")
}

func TestResolveImage_FastTierAndPreference(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)
	ctx := context.Background()
	userID := "media-img-prefs"
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "gemini", "AIza-test"))

	resolve := func(tags ...string) string {
		resolved, _, err := resolveMediaModel(ctx, userID, models.ModelSelector{Tags: tags}, models.ModalityImage)
		require.NoError(t, err)
		return resolved.Definition.ID
	}
	assert.Equal(t, "gemini-3.1-flash-lite-image", resolve("image-gen", "fast"))

	require.NoError(t, setTagPref(ctx, repo, userID, "image-gen", `{"model_id":"gemini-3-pro-image"}`))
	assert.Equal(t, "gemini-3-pro-image", resolve("image-gen", "flagship"), "preference beats the default tier")
	assert.Equal(t, "gemini-3.1-flash-lite-image", resolve("image-gen", "fast"), "preference does not override an explicit fast tier")
}

func setTagPref(ctx context.Context, repo db.Repository, userID, tag, value string) error {
	now := time.Now().UTC()
	return repo.CreateSetting(ctx, &db.Setting{
		ID: uuid.NewString(), UserID: userID, Key: modelprefs.KeyPrefix + tag, Value: value, ValueType: "json", CreatedAt: now, UpdatedAt: now,
	})
}
