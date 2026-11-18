// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"strings"
)

// MediaUnavailableError says a media modality (image, video) cannot be served
// because the user has no provider that carries a model for it. It is a typed
// error so the load_tool gate, the generate_* tools and the driver resolvers
// all surface the SAME text: the message is derived from the catalog, never
// hand-written per call site.
type MediaUnavailableError struct {
	Modality Modality
	Message  string
}

func (e *MediaUnavailableError) Error() string { return e.Message }

// MediaPersonalFundingError says a generation in an org chat could be served
// only by the person's own accounts, which they have not chosen to spend in
// that org (control-plane docs/design/org-provider-accounts.md §4.4, §10). A
// generation has no turn to pause for the question, so it is not made; the
// message says how to allow it. load_tool and the generate_* tools show it
// verbatim, like MediaUnavailableError.
type MediaPersonalFundingError struct {
	Modality Modality
	// OrgOnly: the chat may never use the person's own accounts (org_only).
	OrgOnly bool
	Message string
}

func (e *MediaPersonalFundingError) Error() string { return e.Message }

// NewMediaPersonalFundingError builds the error for a modality.
func NewMediaPersonalFundingError(modality Modality, orgOnly bool) *MediaPersonalFundingError {
	noun := MediaNoun(modality)
	title := strings.ToUpper(noun[:1]) + noun[1:]
	msg := title + " generation was not started: your organization's AI accounts cannot do it, and only your own accounts could. "
	if orgOnly {
		msg += "This chat may use only your organization's accounts, so this is not a failure to retry. Tell the user an org admin can add an account that can."
	} else {
		msg += "You have not chosen to use your own accounts in this organization, so this is not a failure to retry. Tell the user they can allow it in Settings → AI providers, in the organization's section (\"When its accounts can't serve a chat\", or use their own accounts first), or ask an org admin to add an account that can."
	}
	return &MediaPersonalFundingError{Modality: modality, OrgOnly: orgOnly, Message: msg}
}

// NewMediaUnavailableError builds the error for a modality from the catalog.
func NewMediaUnavailableError(registry *ModelRegistry, modality Modality) *MediaUnavailableError {
	return &MediaUnavailableError{Modality: modality, Message: MediaUnavailableMessage(registry, modality)}
}

// mediaProviderLabels name what the user must add for each provider, in the
// words the Settings screen uses. An unlisted provider falls back to its id, so
// a newly added provider still produces a usable message.
var mediaProviderLabels = map[string]string{
	"gemini":              "a Gemini API key",
	"openai":              "an OpenAI API key",
	"codex":               "a ChatGPT (Codex) login",
	"antigravity":         "an Antigravity login",
	ManagedDriverIDString: "Reliant-managed credits",
}

// MediaProviderLabel names what the user must add to use a provider, e.g.
// "a Gemini API key"; an unlisted provider falls back to "a <id> provider".
func MediaProviderLabel(provider string) string {
	if label, ok := mediaProviderLabels[provider]; ok {
		return label
	}
	return fmt.Sprintf("a %s provider", provider)
}

// ManagedDriverIDString is ManagedDriverID as a plain string map key.
const ManagedDriverIDString = string(ManagedDriverID)

// MediaProviders lists, in catalog order and without duplicates, the providers
// that carry at least one model producing the modality.
func MediaProviders(registry *ModelRegistry, modality Modality) []string {
	var providers []string
	seen := map[string]bool{}
	for _, def := range registry.ListAll() {
		if !def.Capabilities.CanOutput(modality) {
			continue
		}
		for _, mapping := range def.Providers {
			if !seen[mapping.Driver] {
				seen[mapping.Driver] = true
				providers = append(providers, mapping.Driver)
			}
		}
	}
	return providers
}

// MediaNoun is the human noun for a modality's generation, e.g. "video".
func MediaNoun(modality Modality) string { return string(modality) }

// MediaUnavailableMessage is the one sentence that tells a user how to unlock a
// modality: what is missing and where to add it. It depends only on the
// catalog, so a provider added to models.yaml shows up in it automatically.
func MediaUnavailableMessage(registry *ModelRegistry, modality Modality) string {
	noun := MediaNoun(modality)
	title := strings.ToUpper(noun[:1]) + noun[1:]

	var needs []string
	for _, provider := range MediaProviders(registry, modality) {
		if label, ok := mediaProviderLabels[provider]; ok {
			needs = append(needs, label)
		} else {
			needs = append(needs, fmt.Sprintf("a %s provider", provider))
		}
	}
	if len(needs) == 0 {
		return fmt.Sprintf("%s generation is not available: no provider in this build serves %s models", title, noun)
	}
	return fmt.Sprintf("%s generation needs %s — add one in Settings → AI providers", title, joinOr(needs))
}

func joinOr(items []string) string {
	switch len(items) {
	case 1:
		return items[0]
	case 2:
		return items[0] + " or " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
