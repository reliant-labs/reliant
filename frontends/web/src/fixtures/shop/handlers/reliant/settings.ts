// Copyright (c) 2025 Reliant Labs

/**
 * SettingsService and CatalogService: Alex is past onboarding (the wizard and
 * the getting-started guide dismissed), shares no analytics, keeps the
 * default shortcuts and preferences, has Anthropic and OpenAI connected, and
 * picks from four models with a smart / default / fast tier each.
 */
import type { ChecklistItemId } from "@/components/Onboarding/types";
import { CatalogService, type ModelInfo } from "@/gen/reliant/v1/catalog_pb";
import { ConfigScope } from "@/gen/reliant/v1/common_pb";
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

/**
 * What Alex has saved, server-side. Besides onboarding, each is a value the
 * app would otherwise write back on a fresh page, which a read-only pack
 * refuses: the getting-started guide's state and its completed items
 * (onboardingChecklistStore loadState heals the server from what the page
 * knows or the data shows), and the appearance page's defaults (it persists
 * every unset preference when it opens). The theme is not saved, so the
 * device's (or the screenshot harness's) theme applies.
 */
const SAVED_SETTINGS: [key: string, value: string][] = [
  ["onboarding.wizard.dismissed", "true"],
  ["onboarding.checklist.panel_state", "dismissed"],
  // Every item of the guide (components/Onboarding/types.ts ChecklistItemId), in current ids: with all
  // of them saved, loadState has no retired id to migrate and detection finds nothing new, so neither writes.
  [
    "onboarding.checklist.completed_items",
    JSON.stringify([
      "connect-machine", "add-api-key", "start-chat", "add-repo", "run-workflow", "deploy-app", "invite-teammate", "take-product-tour",
    ] satisfies ChecklistItemId[]),
  ],
  ["appearance.font", "default"],
  ["appearance.chatFont", "default"],
  ["appearance.editorFont", "default"],
  ["appearance.fontSize", "lg"],
];

export function settingsHandlers() {
  return {
    ...serve(SettingsService, {
      listSettings: () => ({ settings: SAVED_SETTINGS.map(([key, value]) => setting(key, value)), total: SAVED_SETTINGS.length }),
      getPrivacySettings: () => ({ analyticsEnabled: false, crashReportingEnabled: false }),
      // Alex's config is healthy. (The server heals a stale legacy key as it reads, so the rpc
      // declares no NO_SIDE_EFFECTS: it is one of the app pack's named `answers`, app.ts.)
      getConfigHealth: () => ({ errors: [], errorCount: 0, warningCount: 0 }),
      // Every shortcut at its default binding.
      getShortcuts: () => ({ shortcuts: "{}" }),
      // The defaults a new account starts with (hooks/settings-queries.ts DEFAULT_PREFERENCES).
      getPreferences: () => ({
        streamingEnabled: true, worktreeArchiveMode: "ask_me", worktreeDefaultDeleteBranch: false, additional: {}, defaultAutoApprove: false,
        branchCopyUncommittedFilesDefault: false, defaultPlanningMode: false, defaultMcpScope: ConfigScope.PROJECT, defaultWorkflowScope: ConfigScope.PROJECT,
        defaultWorkflow: "builtin://agent", hideBuiltinWorkflows: false, hideBuiltinPresets: false, hiddenWorkflowSlugs: [], hiddenPresetSlugs: [],
      }),
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
      // The workflow catalog's search: nothing published to it matches (the library lists Alex's own and the built-ins).
      searchCatalog: () => ({ entries: [], nextPageToken: "", totalSize: 0, categoryFacets: [] }),
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
