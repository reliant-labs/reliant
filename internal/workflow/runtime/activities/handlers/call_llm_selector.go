// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"slices"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// mediaTags are the tiers that select a model for what it GENERATES (an image,
// a video). A selector naming one asked for that output on purpose.
var mediaTags = []string{"image-gen", "video-gen"}

// agentTurnSelector is the selector an agent turn resolves: a tier selection
// must land on a model that writes text. Tag scoring degrades gracefully, and
// several tiers list image models ([flagship] gpt-image-2.5-sunburst, [fast]
// gpt-image-2.5-flare, both served by codex), so a tier whose text models a
// provider stops serving resolved to an image model, and the turn failed on it
// instead of falling through to the next provider's text model (reliant bug
// 32f76b4d; control-plane #2612). The filter is hard, as compaction's is.
//
// An exact model id is left alone: it is the caller's explicit choice, and
// resolution already names a model that cannot do what was asked. A selector
// that already requires a modality, or names a media tier, is left alone too.
func agentTurnSelector(selector models.ModelSelector) models.ModelSelector {
	if len(selector.Tags) == 0 || selector.RequireOutputModality != "" {
		return selector
	}
	for _, tag := range selector.Tags {
		if slices.Contains(mediaTags, tag) {
			return selector
		}
	}
	selector.RequireOutputModality = models.ModalityText
	return selector
}
