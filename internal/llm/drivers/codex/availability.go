// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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
)

// codexModelsResponse is the subset of GET /codex/models consumed. Recorded
// 2026-10-04 (see testdata/codex_models.json).
type codexModelsResponse struct {
	Models []struct {
		Slug                     string `json:"slug"`
		Visibility               string `json:"visibility"`
		ContextWindow            int    `json:"context_window"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

type availabilityEntry struct {
	report    registry.ProviderAvailability
	err       error
	fetchedAt time.Time
}

var (
	availabilityMu    sync.Mutex
	availabilityCache = map[string]availabilityEntry{}

	// unknownSlugsLogged dedups the "slug has no catalog entry" log: one line per
	// slug per process, so a new Codex model surfaces as a catalog TODO without
	// flooding the log on every TTL refresh.
	unknownSlugsLogged sync.Map
)

// accountKey is a stable, non-reversible cache key for the credential.
func accountKey(accountID, token string) string {
	sum := sha256.Sum256([]byte(accountID + "\x00" + strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:8])
}

// ReportAvailability implements registry.AvailabilityReporter from the account's
// /codex/models catalog (cached per credential):
//
//   - slug maps to our catalog id by api_model;
//   - visibility "hide" means not offered;
//   - context_window overrides the model's window for codex;
//   - reasoning levels are the INTERSECTION of the advertised levels and the
//     ones the API accepts (`ultra` is advertised and 400s).
//
// A model the catalog does not list is NOT servable (the account does not
// serve it). A slug with no catalog entry is not auto-added; it is logged once.
// A failed fetch is an error, which callers treat as fail-open.
func (c *CodexClient) ReportAvailability(ctx context.Context) (registry.ProviderAvailability, error) {
	key := accountKey(c.accountID, c.accessToken)

	availabilityMu.Lock()
	if entry, ok := availabilityCache[key]; ok {
		ttl := availabilityTTL
		if entry.err != nil {
			ttl = availabilityFailureTTL
		}
		if time.Since(entry.fetchedAt) < ttl {
			availabilityMu.Unlock()
			return entry.report, entry.err
		}
	}
	availabilityMu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, availabilityTimeout)
	defer cancel()
	var raw json.RawMessage
	err := c.client.Get(fetchCtx, codexModelsPath+"?client_version="+CodexVersion, nil, &raw)
	var report registry.ProviderAvailability
	if err != nil {
		err = fmt.Errorf("codex models request failed: %w", err)
	} else {
		report, err = parseCodexModels(raw)
	}

	availabilityMu.Lock()
	availabilityCache[key] = availabilityEntry{report: report, err: err, fetchedAt: time.Now()}
	availabilityMu.Unlock()
	return report, err
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
	if reg, err := models.GetRegistry(); err == nil {
		for _, def := range reg.ListModelsByProvider(string(Family)) {
			for _, p := range def.Providers {
				if p.Driver == string(Family) {
					catalog[p.APIModel] = true
				}
			}
		}
	}

	report := registry.ProviderAvailability{
		Models:         make(map[string]models.ModelAvailability, len(parsed.Models)),
		Authoritative:  true,
		UnlistedReason: codexNotServedReason,
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
		report.Models[slug] = models.ModelAvailability{
			Disabled:       m.Visibility == "hide",
			Reason:         codexNotServedReason,
			ContextWindow:  m.ContextWindow,
			ThinkingLevels: models.IntersectCodexLevels(advertised),
		}
	}
	return report, nil
}
