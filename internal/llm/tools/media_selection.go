// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Media tools (generate_video, generate_image) let the LLM choose a model by
// INTENT, not by id: a small tier vocabulary maps to registry tag selectors,
// and an explicit model id is still accepted for when intent is not enough.
// Precedence, highest first:
//
//	model (open param, or a binding that locks it) > tier > default tier
//
// The default tier is "standard", whose selector is the tool's old bound
// default, so an unconfigured call resolves as it always did. The driver layer
// treats that selector as "the user's default tier" and applies the user's
// stored preference for the base tag to it (see drivers.ApplyTierPreference).

// mediaTier is one rung of a media tool's tier vocabulary.
type mediaTier struct {
	Name string
	Tags []string
	// Requires, when set, is a registry tag the resolved model MUST carry.
	// Tag scoring degrades gracefully by design, so [video-gen, cinematic]
	// would otherwise hand a user with no Veo access the standard model and
	// call it cinematic.
	Requires string
}

// mediaKind describes one media tool's selection vocabulary.
type mediaKind struct {
	noun        string
	modality    models.Modality
	param       string
	tiers       []mediaTier
	defaultTier string
}

var videoKind = mediaKind{
	noun:     "video",
	modality: models.ModalityVideo,
	param:    "quality",
	tiers: []mediaTier{
		{Name: "fast", Tags: []string{VideoGenTag, models.TagCheap}, Requires: models.TagCheap},
		{Name: "standard", Tags: []string{VideoGenTag, models.TagFlagship}},
		{Name: "cinematic", Tags: []string{VideoGenTag, "cinematic"}, Requires: "cinematic"},
	},
	defaultTier: "standard",
}

// The image tool already has a `quality` parameter (rendering effort, passed to
// the provider), so its model tier is named `tier`.
var imageKind = mediaKind{
	noun:     "image",
	modality: models.ModalityImage,
	param:    "tier",
	tiers: []mediaTier{
		{Name: "fast", Tags: []string{ImageGenTag, models.TagFast}, Requires: models.TagFast},
		{Name: "standard", Tags: []string{ImageGenTag, models.TagFlagship}},
	},
	defaultTier: "standard",
}

// mediaToolModality maps a tool name to the output modality it needs a
// provider for. Names not listed are not media tools.
var mediaToolModality = map[string]models.Modality{
	ToolGenerateVideo: models.ModalityVideo,
	ToolGenerateImage: models.ModalityImage,
}

func (k mediaKind) tierNames() []string {
	names := make([]string, len(k.tiers))
	for i, tier := range k.tiers {
		names[i] = tier.Name
	}
	return names
}

func (k mediaKind) tier(name string) (mediaTier, bool) {
	for _, tier := range k.tiers {
		if tier.Name == name {
			return tier, true
		}
	}
	return mediaTier{}, false
}

// modelIDs lists the catalog models that produce this kind's modality, in
// catalog order.
func (k mediaKind) modelIDs(registry *models.ModelRegistry) []string {
	var ids []string
	for _, def := range registry.ListAll() {
		if def.Capabilities.CanOutput(k.modality) {
			ids = append(ids, def.ID)
		}
	}
	return ids
}

// mediaChoice is the decision about which selector to hand the driver layer,
// plus what is needed to explain it afterwards.
type mediaChoice struct {
	Selector models.ModelSelector
	// Tier is the tier name when the choice came from one, else "".
	Tier string
	// Requires mirrors mediaTier.Requires.
	Requires string
	// Reason is the human-readable "why", e.g. "quality=cinematic".
	Reason string
}

// chooseMedia applies the precedence rules. explicit is the (possibly bound)
// `model` parameter; tier is the intent parameter.
func chooseMedia(kind mediaKind, explicit models.ModelSelector, tier string) (mediaChoice, error) {
	tier = strings.TrimSpace(strings.ToLower(tier))
	if tier != "" {
		if _, ok := kind.tier(tier); !ok {
			return mediaChoice{}, fmt.Errorf("%s must be one of %s, got %q", kind.param, strings.Join(kind.tierNames(), ", "), tier)
		}
	}

	if explicit.ID != "" || len(explicit.Tags) > 0 || len(explicit.Providers) > 0 {
		reason := "model selector fixed by configuration"
		if explicit.ID != "" {
			registry, err := models.GetRegistry()
			if err != nil {
				return mediaChoice{}, err
			}
			if err := kind.validateModelID(registry, explicit.ID); err != nil {
				return mediaChoice{}, err
			}
			reason = fmt.Sprintf("model=%s", explicit.ID)
		}
		if tier != "" {
			reason += fmt.Sprintf(" (%s=%s ignored: an explicit model wins)", kind.param, tier)
		}
		return mediaChoice{Selector: explicit, Reason: reason}, nil
	}

	reason := fmt.Sprintf("%s=%s", kind.param, tier)
	if tier == "" {
		tier = kind.defaultTier
		reason = fmt.Sprintf("default %s=%s", kind.param, tier)
	}
	chosen, _ := kind.tier(tier)
	return mediaChoice{
		Selector: models.ModelSelector{Tags: slices.Clone(chosen.Tags)},
		Tier:     tier,
		Requires: chosen.Requires,
		Reason:   reason,
	}, nil
}

// validateModelID checks an agent-supplied model id against the catalog's
// models for this modality and, on failure, lists the valid ones.
func (k mediaKind) validateModelID(registry *models.ModelRegistry, id string) error {
	bare := id
	if at := strings.LastIndex(id, "@"); at != -1 {
		bare = id[:at]
	}
	if def, ok := registry.GetDefinition(bare); ok && def.Capabilities.CanOutput(k.modality) {
		return nil
	}
	return fmt.Errorf("%q is not a %s model. Valid %s models: %s. Or omit model and set %s (%s)",
		id, k.noun, k.noun, strings.Join(k.modelIDs(registry), ", "), k.param, strings.Join(k.tierNames(), " | "))
}

// extendMediaSchema rewrites the open `model` property into a plain string with
// the currently valid ids listed. The struct field is a ModelSelector (so a
// human can still bind a full selector), but the agent should see "an id".
func (k mediaKind) extendMediaSchema(s *jsonschema.Schema) {
	if s.Properties == nil {
		return
	}
	desc := fmt.Sprintf("Optional exact %s model id. Usually omit this and set %s instead; use model only when you need one specific model.", k.noun, k.param)
	if registry, err := models.GetRegistry(); err == nil {
		if ids := k.modelIDs(registry); len(ids) > 0 {
			desc += " Valid ids: " + strings.Join(ids, ", ") + "."
		}
	}
	s.Properties.Set("model", &jsonschema.Schema{Type: "string", Description: desc})
}

// mediaResolve resolves a selector and returns the id of the model that came
// back.
type mediaResolve func(ctx context.Context, selector models.ModelSelector) (modelID string, err error)

// verifyTier enforces a tier's Requires tag against the model that actually
// resolved. It returns "" when the choice stands, or a message naming the tiers
// that ARE available with the user's providers.
func (k mediaKind) verifyTier(ctx context.Context, choice mediaChoice, usedModel string, resolve mediaResolve) string {
	if choice.Tier == "" || choice.Requires == "" {
		return ""
	}
	registry, err := models.GetRegistry()
	if err != nil || slices.Contains(registry.TagsOf(usedModel), choice.Requires) {
		return ""
	}

	var available []string
	for _, tier := range k.tiers {
		if tier.Name == choice.Tier {
			continue
		}
		id, err := resolve(ctx, models.ModelSelector{Tags: slices.Clone(tier.Tags)})
		if err != nil {
			continue
		}
		if tier.Requires != "" && !slices.Contains(registry.TagsOf(id), tier.Requires) {
			continue
		}
		available = append(available, tier.Name)
	}

	msg := fmt.Sprintf("%s=%s isn't available with your providers", k.param, choice.Tier)
	if len(available) == 0 {
		return msg + ", and no other tier is either."
	}
	return fmt.Sprintf("%s; %s is. Retry with %s=%s.", msg, strings.Join(available, " and "), k.param, available[0])
}

// videoTierHint names the tiers whose models accept a request the chosen model
// rejected, so "4k isn't supported here" arrives with the way out.
func videoTierHint(registry *models.ModelRegistry, current string, request models.VideoRequestParams) string {
	var tiers []string
	for _, tier := range videoKind.tiers {
		if tier.Name == current {
			continue
		}
		for _, def := range registry.GetModelsByTag(tier.Tags[1]) {
			if def == nil || !def.Capabilities.CanOutput(models.ModalityVideo) {
				continue
			}
			if def.Capabilities.Video.ValidateVideoRequest(def.ID, request) == nil {
				tiers = append(tiers, tier.Name)
				break
			}
		}
	}
	if len(tiers) == 0 {
		return ""
	}
	return fmt.Sprintf(" Tiers that support this: %s (set quality=%s).", strings.Join(tiers, ", "), tiers[0])
}

// chosenLine states which model ran and why, for the tool result.
func chosenLine(modelID, reason string) string {
	if modelID == "" || reason == "" {
		return ""
	}
	return fmt.Sprintf("Chosen: %s via %s.", modelID, reason)
}

// MediaAvailabilityCheck reports whether the user has a provider able to serve
// a media modality. It returns a *models.MediaUnavailableError when not. A nil
// return means available OR unknown: a check that cannot tell must not block.
type MediaAvailabilityCheck func(ctx context.Context, userID string, modality models.Modality) error

var (
	mediaAvailabilityMu    sync.RWMutex
	mediaAvailabilityCheck MediaAvailabilityCheck
)

// SetMediaAvailabilityCheck installs the check load_tool uses to refuse a
// media tool the user cannot run. It is a registered seam, not an import,
// because internal/llm/drivers already imports this package and owns the
// credential lookup; drivers installs it from init. A process that never
// links the driver layer (the daemon runtime) simply has no check.
func SetMediaAvailabilityCheck(check MediaAvailabilityCheck) {
	mediaAvailabilityMu.Lock()
	defer mediaAvailabilityMu.Unlock()
	mediaAvailabilityCheck = check
}

// mediaToolUnavailable returns the shared unavailable message when name is a
// media tool the calling user cannot serve, else "". Both load_tool and the
// tools' own call-time backstop produce the text from the same error.
func mediaToolUnavailable(ctx context.Context, name string) (message string, modality models.Modality) {
	modality, isMedia := mediaToolModality[name]
	if !isMedia {
		return "", ""
	}
	mediaAvailabilityMu.RLock()
	check := mediaAvailabilityCheck
	mediaAvailabilityMu.RUnlock()
	if check == nil {
		return "", modality
	}
	userID, _ := auth.GetUserIDFromContext(ctx)
	if userID == "" {
		return "", modality
	}
	var unavailable *models.MediaUnavailableError
	if err := check(ctx, userID, modality); errors.As(err, &unavailable) {
		return unavailable.Message, modality
	}
	return "", modality
}

// mediaResolveFailure renders a resolver error. The shared unavailable error
// is shown verbatim so the message is identical to load_tool's; anything else
// keeps the tool's own prefix.
func mediaResolveFailure(prefix string, err error) string {
	var unavailable *models.MediaUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Message
	}
	return fmt.Sprintf("%s: %v", prefix, err)
}
