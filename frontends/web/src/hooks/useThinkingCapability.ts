import { useMemo } from "react";
import { useModels, type TierResolution } from "../store/globalDataStore";

interface CatalogModel {
  id: string;
  tags?: string[];
  supportedThinkingLevels?: string[];
}

// Descending capability order. gpt-5.6 adds "max" above "xhigh".
const THINKING_ORDER = ["max", "xhigh", "high", "medium", "low"] as const;

export interface ThinkingCapability {
  modelId?: string;
  supportsThinking: boolean;
  levels: string[];
  defaultLevel: string;
  /**
   * Set when the model param is a tag selector ({tags: [tag]}). A tier owns
   * its effort server-side, so an empty thinking_level must stay empty — the
   * tier decides — rather than be pinned to a concrete level.
   */
  tag?: string;
  /** The effort the tier runs at when thinking_level is empty ("" = none). */
  tierLevel?: string;
}

type Tiers = Record<string, TierResolution> | undefined;

interface ModelSelection {
  modelId?: string;
  tag?: string;
}

/**
 * Resolve a model selector to a catalog model id. A selector is either an
 * explicit id or a tag list ({tags: ["flagship"]}), which is what an untouched
 * model param carries. Tags resolve via the server's tier table (what the
 * backend will actually run for this user); the first-catalog-model-with-tag
 * guess is only a fallback for when tiers have not loaded.
 */
function extractModelSelection(candidate: unknown, models: CatalogModel[], tiers: Tiers): ModelSelection | undefined {
  if (typeof candidate === "string") {
    return candidate ? { modelId: candidate } : undefined;
  }
  if (typeof candidate === "object" && candidate !== null) {
    const selector = candidate as { id?: string; tags?: string[] };
    if (selector.id) return { modelId: selector.id };
    const tag = selector.tags?.[0];
    if (tag) {
      const modelId = tiers?.[tag]?.modelId ?? models.find((model) => model.tags?.includes(tag))?.id;
      return { modelId, tag };
    }
  }
  return undefined;
}

function findModelSelectionForThinkingField(
  name: string,
  models: CatalogModel[],
  tiers: Tiers,
  formValues?: Record<string, unknown>,
): ModelSelection | undefined {
  if (!formValues) return undefined;

  const keys: string[] = [];
  if (name === "thinking_level") {
    keys.push("model");
  } else if (name.endsWith(".thinking_level")) {
    const prefix = name.slice(0, -".thinking_level".length);
    keys.push(`${prefix}.model`, "model");
  }

  for (const key of keys) {
    const selection = extractModelSelection(formValues[key], models, tiers);
    if (selection?.modelId) return selection;
  }

  return undefined;
}

/**
 * The effort a model runs at when chosen by id (no tier): medium where
 * supported, otherwise the highest level. Mirrors PreferredThinkingLevel in
 * internal/llm/models/thinking_policy.go. `levels` must be sorted descending
 * (THINKING_ORDER), so levels[0] is the highest.
 */
export function preferredThinkingLevel(levels: string[]): string {
  if (levels.includes("medium")) return "medium";
  return levels[0] ?? "";
}

export function resolveThinkingCapabilityForModel(modelId: string | undefined, models: CatalogModel[]): ThinkingCapability {
  if (!modelId) {
    return {
      modelId: undefined,
      supportsThinking: false,
      levels: [],
      defaultLevel: "",
    };
  }

  // Catalog ids are "modelId@driverId"; a selector may carry either form.
  const model =
    models.find((m) => m.id === modelId) ??
    models.find((m) => m.id.split("@")[0] === modelId);
  const levels = (model?.supportedThinkingLevels || []).slice();

  levels.sort((a, b) => {
    const ai = THINKING_ORDER.indexOf(a as (typeof THINKING_ORDER)[number]);
    const bi = THINKING_ORDER.indexOf(b as (typeof THINKING_ORDER)[number]);
    const ar = ai === -1 ? Number.MAX_SAFE_INTEGER : ai;
    const br = bi === -1 ? Number.MAX_SAFE_INTEGER : bi;
    if (ar !== br) return ar - br;
    return a.localeCompare(b);
  });

  return {
    modelId,
    supportsThinking: levels.length > 0,
    levels,
    defaultLevel: preferredThinkingLevel(levels),
  };
}

/**
 * Thinking capability for a selector: the model it reaches, plus — for a tag
 * selector — the tier's own effort.
 */
export function resolveThinkingCapabilityForSelector(
  selector: unknown,
  models: CatalogModel[],
  tiers: Tiers,
): ThinkingCapability {
  const selection = extractModelSelection(selector, models, tiers);
  return withTier(resolveThinkingCapabilityForModel(selection?.modelId, models), selection?.tag, tiers);
}

function withTier(capability: ThinkingCapability, tag: string | undefined, tiers: Tiers): ThinkingCapability {
  if (!tag) return capability;
  return { ...capability, tag, tierLevel: tiers?.[tag]?.thinkingLevel };
}

export function useThinkingCapability(name: string, formValues?: Record<string, unknown>): ThinkingCapability {
  const { models, tiers } = useModels();

  return useMemo(() => {
    const selection = findModelSelectionForThinkingField(name, models, tiers, formValues);
    return withTier(resolveThinkingCapabilityForModel(selection?.modelId, models), selection?.tag, tiers);
  }, [models, tiers, name, formValues]);
}

/** Label for an empty thinking_level: the tier's effort when a tier decides it. */
export function autoThinkingLabel(capability: ThinkingCapability): string | undefined {
  if (!capability.tag) return undefined;
  return capability.tierLevel ? `Auto (${capability.tierLevel})` : "Auto";
}

export function reconcileThinkingLevel(level: string, capability: ThinkingCapability): string {
  if (!capability.supportsThinking || capability.levels.length === 0) {
    return "";
  }
  if (level && capability.levels.includes(level)) {
    return level;
  }
  // A tag selector's effort belongs to the tier: an empty or unsupported level
  // falls back to "" (the tier's own level), never to a pinned concrete one —
  // an explicit thinking_level would override the tier server-side.
  if (capability.tag) {
    return "";
  }
  return capability.defaultLevel;
}
