// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/reliant-labs/reliant/internal/llm/drivers/catalogcache"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// codexModelsPath is the per-account catalog the Codex backend serves. It is
// version-gated: client_version must be a current codex-tui release.
const codexModelsPath = "models"

const (
	availabilityTTL        = 5 * time.Minute
	availabilityFailureTTL = 30 * time.Second
	availabilityTimeout    = 4 * time.Second
	// availabilityMaxStale is how long a report past its TTL is still served
	// while a background fetch replaces it (catalogcache): long enough that a
	// person coming back to a chat within a working day never waits on
	// /codex/models. Every stale answer triggers the refresh, so it serves one
	// resolution at most; a model the account stopped serving meanwhile is
	// caught by the refusal itself, whose 400 "model is not supported" evicts
	// the report (forgetCatalogOnRefusal).
	availabilityMaxStale = 6 * time.Hour
)

// codexModelsResponse is the subset of GET /codex/models consumed (see
// testdata/codex_models.json and its provenance on recordedReport).
//
// The two window fields mean what Codex CLI makes them mean (openai/codex
// codex-rs/protocol/src/openai_models.rs, models-manager/src/model_info.rs):
// context_window is the window a session runs at by DEFAULT (Codex CLI
// auto-compacts against it), and max_context_window is "the maximum context
// window allowed for config overrides" — the most the backend serves. For the
// GPT-5.6 family and GPT-6 those are 272000 and 872000, and prod accepted
// 698,604-token gpt-5.6-terra prompts, so the backend's limit is
// max_context_window. context_window is only the fallback when max is absent.
type codexModelsResponse struct {
	Models []struct {
		Slug                     string `json:"slug"`
		Visibility               string `json:"visibility"`
		ContextWindow            int    `json:"context_window"`
		MaxContextWindow         int    `json:"max_context_window"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

var (
	// availabilityCache holds each account's /codex/models report. Resolution
	// reads it on the request path (SendMessage, StartChat, every call_llm),
	// so a due report is served stale and replaced in the background rather
	// than waited for: in prod (2026-11-26) the synchronous refetch was about
	// half of all SendMessage calls, p50 443ms, p95 1.1s.
	availabilityCache = catalogcache.New[registry.ProviderAvailability](catalogcache.Policy{
		TTL:        availabilityTTL,
		MaxStale:   availabilityMaxStale,
		FailureTTL: availabilityFailureTTL,
		Timeout:    availabilityTimeout,
		Verdict:    func(err error) bool { return errors.Is(err, registry.ErrCredentialRejected) },
	})

	// unknownSlugsLogged dedups the "slug has no catalog entry" log: one line per
	// slug per process, so a new Codex model surfaces as a catalog TODO without
	// flooding the log on every TTL refresh.
	unknownSlugsLogged sync.Map
)

// availabilityKey is a stable, non-reversible cache key for everything the
// catalog depends on: the backend that serves it and the credential it is
// asked with. A report fetched from one backend is never served for another,
// even for the same token.
func (c *CodexClient) availabilityKey() string {
	sum := sha256.Sum256([]byte(c.baseURL + "\x00" + c.accountID + "\x00" + strings.TrimSpace(c.accessToken)))
	return hex.EncodeToString(sum[:8])
}

// ReportAvailability implements registry.AvailabilityReporter from the account's
// /codex/models catalog (cached per backend and credential, stale-while-revalidate;
// see availabilityCache):
//
//   - slug maps to our catalog id by api_model;
//   - visibility "hide" means not offered;
//   - max_context_window (context_window when absent) is the limit the backend
//     advertises; it caps the codex prompt ceiling when lower than the
//     catalog's (see models.ProviderPromptCeiling);
//   - reasoning levels are the INTERSECTION of the advertised levels and the
//     ones the API accepts (`ultra` is advertised and 400s).
//
// An unlisted chat model is not servable. Image generation uses a separate
// endpoint, so absence from this chat catalog is not a verdict on image
// models mapped to that endpoint. An unknown slug is only logged.
// A failed fetch is an error: a credential refusal is a
// registry.CredentialRejectedError (the provider is unavailable), anything
// else an outage callers fail open on.
func (c *CodexClient) ReportAvailability(ctx context.Context) (registry.ProviderAvailability, error) {
	return availabilityCache.Get(ctx, c.availabilityKey(), c.fetchAvailability)
}

// fetchAvailability asks the backend for the account's catalog.
func (c *CodexClient) fetchAvailability(ctx context.Context) (registry.ProviderAvailability, error) {
	var raw json.RawMessage
	if err := c.client.Get(ctx, codexModelsPath+"?client_version="+CodexVersion, nil, &raw); err != nil {
		return registry.ProviderAvailability{}, modelsRequestError(err)
	}
	return parseCodexModels(raw)
}

// forgetCatalogOnRefusal evicts the account's cached catalog when the backend
// refused a request for its model ("The '<model>' model is not supported when
// using Codex with a ChatGPT account."): the cached report offered a model the
// account no longer serves, so the next resolution asks the backend again and
// moves off it rather than repeating the refusal until the report expires.
func (c *CodexClient) forgetCatalogOnRefusal(err error) {
	if !isModelRefusal(err) {
		return
	}
	availabilityCache.Evict(c.availabilityKey())
	logging.Info("codex refused a model its cached catalog offered; catalog evicted", "model", c.options.Model.APIModel)
}

// isModelRefusal reports whether err is the Codex backend's 400 for a model
// the account does not serve.
func isModelRefusal(err error) bool {
	var apiErr *openai.Error
	if err == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	return strings.Contains(err.Error(), "model is not supported")
}

// modelsRequestError classifies a failed GET /codex/models. A 400, 401 or 403
// is the Codex backend refusing the credential every request carries (by now
// past the refresh transport, which refreshes an expired token and retries a
// 401 once after reloading rotated tokens), so it is a
// registry.CredentialRejectedError and Codex becomes unavailable. A transport
// error, timeout, 429 or 5xx is an outage, and callers fail open.
func modelsRequestError(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		detail := AugmentAPIError(err).Error()
		if apiErr.Message != "" {
			detail = apiErr.Message
		}
		if rejected := registry.RejectedCredential(apiErr.StatusCode, detail); rejected != nil {
			return fmt.Errorf("codex models request: %w", rejected)
		}
	}
	return fmt.Errorf("codex models request failed: %w", err)
}

// GetAvailableModels implements registry.ModelLister for the model picker,
// derived from the same report resolution uses.
func (c *CodexClient) GetAvailableModels(ctx context.Context) ([]models.ModelInfo, error) {
	report, err := c.ReportAvailability(ctx)
	if err != nil {
		return nil, err
	}
	return registry.ApplyAvailability(models.MustGetRegistry().ModelsForDriver(string(Family)), report), nil
}

// codexNotServedReason explains a catalog model the account's /codex/models does
// not offer.
const codexNotServedReason = "not available on your ChatGPT plan's Codex backend"

func parseCodexModels(body []byte) (registry.ProviderAvailability, error) {
	var parsed codexModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return registry.ProviderAvailability{}, fmt.Errorf("failed to parse codex models response: %w", err)
	}

	catalog := map[string]bool{}
	imageModels := map[string]bool{}
	if reg, err := models.GetRegistry(); err == nil {
		for _, def := range reg.ListModelsByProvider(string(Family)) {
			for _, p := range def.Providers {
				if p.Driver == string(Family) {
					catalog[p.APIModel] = true
					if def.Capabilities.CanOutput(models.ModalityImage) && !def.Capabilities.CanOutput(models.ModalityText) {
						imageModels[p.APIModel] = true
					}
				}
			}
		}
	}

	report := registry.ProviderAvailability{
		Models:         make(map[string]models.ModelAvailability, len(parsed.Models)),
		Authoritative:  true,
		UnlistedReason: codexNotServedReason,
	}
	// Keep the verified static mappings for the separate image endpoint.
	// A listed entry below can still explicitly disable a model.
	for slug := range imageModels {
		report.Models[slug] = models.ModelAvailability{}
	}
	for _, m := range parsed.Models {
		slug := strings.TrimSpace(m.Slug)
		if slug == "" {
			continue
		}
		if !catalog[slug] {
			if m.Visibility != "hide" {
				if _, seen := unknownSlugsLogged.LoadOrStore(slug, true); !seen {
					logging.Warn("codex /models lists a model with no catalog entry; add it to models.yaml to offer it",
						"slug", slug)
				}
			}
			continue
		}
		advertised := make([]string, 0, len(m.SupportedReasoningLevels))
		for _, l := range m.SupportedReasoningLevels {
			advertised = append(advertised, l.Effort)
		}
		limit := m.MaxContextWindow
		if limit <= 0 {
			limit = m.ContextWindow
		}
		report.Models[slug] = models.ModelAvailability{
			Disabled:       m.Visibility == "hide",
			Reason:         codexNotServedReason,
			ContextWindow:  limit,
			ThinkingLevels: models.IntersectCodexLevels(advertised),
		}
	}
	return report, nil
}
