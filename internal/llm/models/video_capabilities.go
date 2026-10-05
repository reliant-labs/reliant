// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"slices"
	"strings"
)

// VideoCapabilities declares what one video-generation model accepts. It is
// the single place request parameters are validated, so a tool never carries
// per-model if-chains and an error can name the model and the allowed values.
type VideoCapabilities struct {
	// Durations lists the allowed clip lengths in seconds.
	Durations []int `yaml:"durations,omitempty" json:"durations,omitempty" mapstructure:"durations"`
	// Resolutions lists allowed values such as "720p".
	Resolutions []string `yaml:"resolutions,omitempty" json:"resolutions,omitempty" mapstructure:"resolutions"`
	// NativeResolutions is the subset of Resolutions the model renders at
	// natively; anything else in Resolutions is upscaled. Empty means every
	// listed resolution is native. The distinction is the whole difference
	// between "1080p" on Veo (rendered at 1080p) and on Omni (rendered at
	// 720p and upscaled), so it is what lets an agent pick the right model
	// for sharp high-resolution output instead of treating them as equal.
	NativeResolutions []string `yaml:"native_resolutions,omitempty" json:"native_resolutions,omitempty" mapstructure:"native_resolutions"`
	// Aspects lists allowed aspect ratios such as "16:9".
	Aspects []string `yaml:"aspects,omitempty" json:"aspects,omitempty" mapstructure:"aspects"`
	// MaxReferenceImages is the reference-image cap; 0 means unsupported.
	MaxReferenceImages int `yaml:"max_reference_images,omitempty" json:"max_reference_images,omitempty" mapstructure:"max_reference_images"`
	// SupportsEdit means a prior clip can be refined conversationally.
	SupportsEdit bool `yaml:"supports_edit,omitempty" json:"supports_edit,omitempty" mapstructure:"supports_edit"`
	// SupportsExtend means a prior clip can be continued.
	SupportsExtend bool `yaml:"supports_extend,omitempty" json:"supports_extend,omitempty" mapstructure:"supports_extend"`
	// SupportsNegativePrompt means a native negative-prompt field exists.
	SupportsNegativePrompt bool `yaml:"supports_negative_prompt,omitempty" json:"supports_negative_prompt,omitempty" mapstructure:"supports_negative_prompt"`
	// FullResolutionDuration, when non-zero, is the only duration allowed for
	// HighResolutions (Veo: 1080p and 4k require 8s).
	FullResolutionDuration int `yaml:"full_resolution_duration,omitempty" json:"full_resolution_duration,omitempty" mapstructure:"full_resolution_duration"`
	// HighResolutions are the resolutions bound by FullResolutionDuration.
	HighResolutions []string `yaml:"high_resolutions,omitempty" json:"high_resolutions,omitempty" mapstructure:"high_resolutions"`
}

// VideoRequestParams is the model-independent shape ValidateVideoRequest checks.
// Zero values mean "not specified, use the provider default".
type VideoRequestParams struct {
	DurationSeconds int
	Resolution      string
	AspectRatio     string
	ReferenceImages int
	NegativePrompt  bool
	Edit            bool
	Extend          bool
}

// ValidateVideoRequest checks params against the declared capabilities and
// returns an actionable error naming modelID, the parameter and the allowed
// values.
// IsUpscaled reports whether the model reaches resolution by upscaling a lower
// native render rather than generating at it.
func (v *VideoCapabilities) IsUpscaled(resolution string) bool {
	if v == nil || len(v.NativeResolutions) == 0 || resolution == "" {
		return false
	}
	return !slices.Contains(v.NativeResolutions, resolution)
}

func (v *VideoCapabilities) ValidateVideoRequest(modelID string, p VideoRequestParams) error {
	if v == nil {
		return fmt.Errorf("model %s declares no video capabilities", modelID)
	}
	if p.DurationSeconds != 0 && len(v.Durations) > 0 && !slices.Contains(v.Durations, p.DurationSeconds) {
		return fmt.Errorf("model %s does not support duration_seconds=%d; allowed: %s", modelID, p.DurationSeconds, joinInts(v.Durations))
	}
	if p.Resolution != "" && len(v.Resolutions) > 0 && !slices.Contains(v.Resolutions, p.Resolution) {
		return fmt.Errorf("model %s does not support resolution=%q; allowed: %s", modelID, p.Resolution, strings.Join(v.Resolutions, ", "))
	}
	if p.AspectRatio != "" && len(v.Aspects) > 0 && !slices.Contains(v.Aspects, p.AspectRatio) {
		return fmt.Errorf("model %s does not support aspect_ratio=%q; allowed: %s", modelID, p.AspectRatio, strings.Join(v.Aspects, ", "))
	}
	if v.FullResolutionDuration != 0 && slices.Contains(v.HighResolutions, p.Resolution) &&
		p.DurationSeconds != 0 && p.DurationSeconds != v.FullResolutionDuration {
		return fmt.Errorf("model %s requires duration_seconds=%d for %s, got %d", modelID, v.FullResolutionDuration, p.Resolution, p.DurationSeconds)
	}
	if p.ReferenceImages > 0 && v.FullResolutionDuration != 0 &&
		p.DurationSeconds != 0 && p.DurationSeconds != v.FullResolutionDuration {
		return fmt.Errorf("model %s requires duration_seconds=%d when reference images are used, got %d", modelID, v.FullResolutionDuration, p.DurationSeconds)
	}
	if p.ReferenceImages > v.MaxReferenceImages {
		if v.MaxReferenceImages == 0 {
			return fmt.Errorf("model %s does not support reference images", modelID)
		}
		return fmt.Errorf("model %s accepts at most %d reference images, got %d", modelID, v.MaxReferenceImages, p.ReferenceImages)
	}
	if p.NegativePrompt && !v.SupportsNegativePrompt {
		return fmt.Errorf("model %s has no native negative_prompt; describe what to avoid in the prompt instead", modelID)
	}
	if p.Edit && !v.SupportsEdit {
		return fmt.Errorf("model %s cannot edit a previous video; regenerate with a full prompt or use a model that supports edit", modelID)
	}
	if p.Extend && !v.SupportsExtend {
		return fmt.Errorf("model %s cannot extend a previous video", modelID)
	}
	return nil
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ", ")
}
