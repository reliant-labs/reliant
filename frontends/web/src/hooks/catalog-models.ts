// Copyright (c) 2025 Reliant Labs

/**
 * CatalogService's model list, read imperatively.
 *
 * `globalDataStore` mirrors the picker's model list (refetched by the
 * `daemon_local_models` stream signal), so it fetches outside React and the
 * generated hooks' cache. `hooks/catalog-queries.ts` is the integration
 * catalog; this is the other half of CatalogService the web reads.
 */

import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { CatalogService } from "../gen/reliant/v1/catalog_pb";

const catalog = () => connectClient(CatalogService);

// Human-readable driver display names, mirrors the backend's
// getDriverDisplayName so provider grouping in the picker is stable even
// when a model is only known via ListAvailableModels (no catalog match).
const DRIVER_DISPLAY_NAMES: Record<string, string> = {
  anthropic: "Anthropic API",
  claude: "Claude subscription",
  openai: "OpenAI",
  codex: "Codex",
  gemini: "Google AI",
  reliant: "Reliant",
  openrouter: "OpenRouter",
  copilot: "GitHub Copilot",
  local: "Local",
};

export function driverDisplayName(driverId: string): string {
  return DRIVER_DISPLAY_NAMES[driverId] || driverId;
}

// Raw per-account available-models list (ListAvailableModels RPC). One entry
// per {model, provider}, each carrying an `enabled` flag reflecting whether
// the caller's account may actually run it (e.g. a dynamic provider like
// Copilot reports its account policy). The id here is the base model id and
// `provider` is the raw driver; callers that need the app-wide model id
// should combine them as `${id}@${provider}`.
async function listAvailableModels() {
  const response = await catalog().listAvailableModels({});
  return response.models.map((m) => ({
    id: m.id,
    displayName: m.displayName,
    provider: m.provider,
    family: m.family,
    apiModel: m.apiModel,
    contextWindow: Number(m.contextWindow),
    defaultMaxTokens: Number(m.defaultMaxTokens),
    canReason: m.canReason,
    supportsAttachments: m.supportsAttachments,
    supportsTools: m.supportsTools,
    supportsCaching: m.supportsCaching,
    tags: m.tags || [],
    enabled: m.enabled,
  }));
}

// Catalog list with full metadata (ListModels RPC). Each model is returned
// once per available driver, id formatted as "modelId@driverId".
async function listCatalogModels() {
  const response = await catalog().listModels({});
  return {
    models: response.models.map((m) => ({
      id: m.id,
      name: m.name,
      provider: m.provider,
      driverId: m.driverId,
      canReason: m.canReason,
      supportedThinkingLevels: m.supportedThinkingLevels,
      supportsTemperature: m.supportsTemperature,
      contextWindow: Number(m.contextWindow),
      defaultCompactionThreshold: Number(m.defaultCompactionThreshold),
      maxCompactionThreshold: Number(m.maxCompactionThreshold),
      local: m.local
        ? {
            daemonId: m.local.daemonId,
            machineName: m.local.machineName,
            endpointId: m.local.endpointId,
            endpointKind: m.local.endpointKind,
            online: m.local.online,
          }
        : undefined,
      capabilities: m.capabilities,
      tags: m.tags || [],
    })),
    total: response.total,
    // What each tag selector ({tags: [tag]}) resolves to for this user:
    // the model AND the effort the tier runs at. Authoritative — the web
    // must not guess a tag's model from the (provider-sorted) list.
    tiers: Object.fromEntries(
      (response.tiers || []).map((t) => [
        t.tag,
        { modelId: t.modelId, thinkingLevel: t.thinkingLevel },
      ])
    ) as Record<string, { modelId: string; thinkingLevel: string }>,
  };
}

/**
 * Authoritative model list for the picker, merging the two catalog RPCs:
 *
 *   - ListModels (catalog) provides deterministic ordering (provider +
 *     priority) and the full metadata AvailableModelInfo omits — notably
 *     supportedThinkingLevels, which many models customize and cannot be
 *     derived from canReason. The catalog is already gated on per-account
 *     availability, so its entries are exactly the enabled known models.
 *   - ListAvailableModels contributes the per-account `enabled` view and any
 *     dynamic provider models (e.g. account-specific Copilot models) that
 *     are not in the static catalog. Its entries are unordered (server-side
 *     map iteration), so we only use it to append the extras the catalog
 *     lacks; disabled entries are dropped, matching the existing omit UX.
 *
 * Fails open: if either RPC fails we degrade to whatever succeeded, so the
 * picker never blanks. Selection semantics are unchanged — every model id is
 * "modelId@driverId".
 */
export async function fetchModelList() {
  const [availableResult, catalogResult] = await Promise.allSettled([
    listAvailableModels(),
    listCatalogModels(),
  ]);

  const catalogModels =
    catalogResult.status === "fulfilled" ? catalogResult.value.models : [];
  const tiers =
    catalogResult.status === "fulfilled" ? catalogResult.value.tiers : {};
  const catalogIds = new Set(catalogModels.map((m) => m.id));

  // Dynamic/per-account models surfaced by ListAvailableModels that the
  // static catalog does not know about. Enabled-only, and only the extras.
  const extras =
    availableResult.status === "fulfilled"
      ? availableResult.value
          .filter((m) => m.enabled)
          .map((m) => ({
            id: `${m.id}@${m.provider}`, // app-wide id; provider is the driver
            displayName: m.displayName,
            driver: m.provider,
            supportsTools: m.supportsTools,
            supportsCaching: m.supportsCaching,
            supportsAttachments: m.supportsAttachments,
            canReason: m.canReason,
            tags: m.tags,
          }))
          .filter((m) => !catalogIds.has(m.id))
          // Deterministic ordering for the extras (catalog is already sorted).
          .sort((a, b) =>
            a.driver === b.driver
              ? a.displayName.localeCompare(b.displayName)
              : a.driver.localeCompare(b.driver)
          )
          .map((m) => ({
            id: m.id,
            name: m.displayName,
            provider: driverDisplayName(m.driver),
            driverId: m.driver,
            canReason: m.canReason,
            // AvailableModelInfo carries no thinking levels; leave undefined
            // (treated as no-thinking) for models absent from the catalog.
            supportedThinkingLevels: undefined as string[] | undefined,
            // Synthesize capabilities from the flags, mirroring the backend's
            // capabilitiesToStrings.
            capabilities: [
              m.supportsTools ? "tools" : null,
              m.supportsCaching ? "caching" : null,
              m.supportsAttachments ? "attachments" : null,
              m.canReason ? "reasoning" : null,
            ].filter((c): c is string => c !== null),
            tags: m.tags,
          }))
      : [];

  if (availableResult.status !== "fulfilled") {
    logger.warn(
      "[fetchModelList] ListAvailableModels failed; using catalog list only",
      availableResult.reason
    );
  }

  const models = [...catalogModels, ...extras];
  return { models, total: models.length, tiers };
}
