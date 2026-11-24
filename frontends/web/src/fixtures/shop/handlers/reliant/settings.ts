// Copyright (c) 2025 Reliant Labs

/**
 * SettingsService and CatalogService: Alex is past onboarding, shares no
 * analytics, has Anthropic and OpenAI connected, and picks from four models
 * with a smart / default / fast tier each.
 */
import { CatalogService, type ModelInfo } from "@/gen/reliant/v1/catalog_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";

import { DAY } from "../../shopStory";
import { isoAgo } from "../../workspace";
import { serve } from "../serve";

const setting = (key: string, value: string) => ({ id: key, key, value, valueType: "string", createdAt: isoAgo(DAY), updatedAt: isoAgo(DAY) });

const model = (id: string, name: string, provider: string, tags: string[], reason: boolean) => ({
  id, name, provider, capabilities: ["text", "tools"], contextWindow: 200_000n, defaultMaxTokens: 16_000n, canReason: reason,
  supportsAttachments: true, driverId: provider, tags, supportsTools: true, supportsCaching: true,
  supportedThinkingLevels: reason ? ["low", "medium", "high"] : [], supportsTemperature: !reason,
  defaultCompactionThreshold: 120_000n, maxCompactionThreshold: 180_000n,
}) satisfies Partial<ModelInfo>;

const MODELS = [
  model("claude-opus-5-5", "Claude Opus 5.5", "anthropic", ["smart"], true),
  model("claude-sonnet-5", "Claude Sonnet 5", "anthropic", ["default"], true),
  model("claude-haiku-4-5", "Claude Haiku 4.5", "anthropic", ["fast"], false),
  model("gpt-5", "GPT-5", "openai", [], true),
];

export function settingsHandlers() {
  return {
    ...serve(SettingsService, {
      listSettings: () => ({ settings: [setting("onboarding.wizard.dismissed", "true")], total: 1 }),
      getPrivacySettings: () => ({ analyticsEnabled: false, crashReportingEnabled: false }),
      getProviderStatuses: () => ({
        providers: [
          { provider: "anthropic", configured: true, displayName: "Anthropic", outputModalities: ["text"], accountCount: 1 },
          { provider: "openai", configured: true, displayName: "OpenAI", outputModalities: ["text"], accountCount: 1 },
        ],
      }),
    }),
    ...serve(CatalogService, {
      listModels: () => ({
        models: MODELS, total: MODELS.length,
        tiers: [
          { tag: "smart", modelId: "claude-opus-5-5", thinkingLevel: "high" },
          { tag: "default", modelId: "claude-sonnet-5", thinkingLevel: "medium" },
          { tag: "fast", modelId: "claude-haiku-4-5", thinkingLevel: "" },
        ],
      }),
      listAvailableModels: () => ({
        models: MODELS.map((m) => ({
          id: m.id, displayName: m.name, provider: m.provider, family: m.provider, apiModel: m.id, contextWindow: m.contextWindow,
          defaultMaxTokens: m.defaultMaxTokens, canReason: m.canReason, supportsAttachments: true, supportsTools: true,
          supportsCaching: true, tags: m.tags, enabled: true,
        })),
      }),
    }),
  };
}
