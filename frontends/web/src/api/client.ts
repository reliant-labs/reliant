import { getCatalogClient } from "./grpc-client";
import { settingsGrpc } from "./settings-grpc";
import { addProviderApiKey, disconnectProvider } from "../hooks/provider-account-queries";
import { logger } from "../lib/logger";

// Re-export types for consumers
export type { Chat, Message, ContentBlock, Attachment } from "../types/chat";
export { ChatState } from "../types/chat";

export const api = {
  models: {
    // Human-readable driver display names, mirrors the backend's
    // getDriverDisplayName so provider grouping in the picker is stable even
    // when a model is only known via ListAvailableModels (no catalog match).
    _driverDisplayName: (driverId: string): string => {
      const names: Record<string, string> = {
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
      return names[driverId] || driverId;
    },

    // Raw per-account available-models list (ListAvailableModels RPC). One entry
    // per {model, provider}, each carrying an `enabled` flag reflecting whether
    // the caller's account may actually run it (e.g. a dynamic provider like
    // Copilot reports its account policy). The id here is the base model id and
    // `provider` is the raw driver; callers that need the app-wide model id
    // should combine them as `${id}@${provider}`.
    listAvailable: async () => {
      const client = getCatalogClient();
      const response = await client.listAvailableModels({});
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
    },

    // Catalog list with full metadata (ListModels RPC). Each model is returned
    // once per available driver, id formatted as "modelId@driverId".
    listCatalog: async () => {
      const client = getCatalogClient();
      const response = await client.listModels({});
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
    },

    // Authoritative model list for the picker, merging the two catalog RPCs:
    //
    //   - ListModels (catalog) provides deterministic ordering (provider +
    //     priority) and the full metadata AvailableModelInfo omits — notably
    //     supportedThinkingLevels, which many models customize and cannot be
    //     derived from canReason. The catalog is already gated on per-account
    //     availability, so its entries are exactly the enabled known models.
    //   - ListAvailableModels contributes the per-account `enabled` view and any
    //     dynamic provider models (e.g. account-specific Copilot models) that
    //     are not in the static catalog. Its entries are unordered (server-side
    //     map iteration), so we only use it to append the extras the catalog
    //     lacks; disabled entries are dropped, matching the existing omit UX.
    //
    // Fails open: if either RPC fails we degrade to whatever succeeded, so the
    // picker never blanks. Selection semantics are unchanged — every model id is
    // "modelId@driverId".
    list: async () => {
      const [availableResult, catalogResult] = await Promise.allSettled([
        api.models.listAvailable(),
        api.models.listCatalog(),
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
                provider: api.models._driverDisplayName(m.driver),
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
          "[api.models.list] ListAvailableModels failed; using catalog list only",
          availableResult.reason
        );
      }

      const models = [...catalogModels, ...extras];
      return { models, total: models.length, tiers };
    },
    listByProvider: async (provider: string) => {
      const client = getCatalogClient();
      const response = await client.listModelsByProvider({ provider });
      return response.models.map((m) => ({
        id: m.id,
        name: m.name,
        provider: m.provider,
        contextWindow: Number(m.contextWindow),
        defaultMaxTokens: Number(m.defaultMaxTokens),
      }));
    },
  },

  tools: {
    list: async () => {
      const client = getCatalogClient();
      const response = await client.listTools({});
      return {
        tools: response.tools.map((t) => ({
          name: t.name,
          description: t.description,
          category: t.category,
        })),
        total: response.total,
      };
    },
  },

  settings: {
    getShortcuts: async () => {
      const shortcuts = await settingsGrpc.getShortcuts();
      return { shortcuts };
    },

    updateShortcuts: async (shortcuts: string) => {
      const result = await settingsGrpc.updateShortcuts(shortcuts);
      return { message: result.message };
    },

    getProviders: async () => {
      const providers = await settingsGrpc.getProviderStatuses();
      return providers.map((p) => ({
        provider: p.provider,
        configured: p.configured,
        hasApiKey: p.configured,
        accountCount: p.account_count,
        displayName: p.display_name,
      }));
    },

    /**
     * Adds an API key as a new account of the provider (ProviderAccountService.AddProviderAPIKey,
     * which validates it first). The same key twice is one account. Rejects an empty key: removing
     * a provider is {@link deleteProvider}.
     */
    updateProvider: async (provider: string, apiKey: string) => {
      if (!apiKey.trim()) {
        throw new Error('An API key is required.');
      }
      const { created } = await addProviderApiKey(provider, apiKey.trim());
      return { message: created ? 'API key added' : 'That key is already connected' };
    },

    /** Removes every account of the provider (it becomes disconnected). */
    deleteProvider: async (provider: string) => {
      await disconnectProvider(provider);
      return { message: 'Disconnected' };
    },

    validateProviderAPIKey: async (provider: string, apiKey: string) => {
      return settingsGrpc.validateProviderAPIKey(provider, apiKey);
    },

    syncReliantProvider: async (forceRotate = false) => {
      return settingsGrpc.syncReliantProvider(forceRotate);
    },

    listSettings: async (projectId?: string) => {
      return settingsGrpc.listSettings(projectId);
    },

    batchUpsertSettings: async (
      settings: Array<{ key: string; value: string; valueType?: string }>,
      projectId?: string
    ) => {
      return settingsGrpc.batchUpsertSettings(settings, projectId);
    },

    deleteSetting: async (key: string, projectId?: string) => {
      return settingsGrpc.deleteSetting(key, projectId);
    },
  },
};