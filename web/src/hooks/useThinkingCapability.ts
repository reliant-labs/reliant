import { useMemo } from "react";
import { useModels } from "../store/globalDataStore";

interface CatalogModel {
  id: string;
  tags?: string[];
  supportedThinkingLevels?: string[];
}

// Descending capability order. gpt-5.6 adds "ultra" and "max" above "xhigh".
const THINKING_ORDER = ["ultra", "max", "xhigh", "high", "medium", "low"] as const;

export interface ThinkingCapability {
  modelId?: string;
  supportsThinking: boolean;
  levels: string[];
  defaultLevel: string;
}

/**
 * Resolve a model selector to a catalog model id. A selector is either an
 * explicit id or a tag list ({tags: ["flagship"]}), which is what an untouched
 * model param carries — resolve tags the same way the backend does, to the
 * first catalog model carrying the tag.
 */
function extractModelId(candidate: unknown, models: CatalogModel[]): string | undefined {
  if (typeof candidate === "string") {
    return candidate || undefined;
  }
  if (typeof candidate === "object" && candidate !== null) {
    const selector = candidate as { id?: string; tags?: string[] };
    if (selector.id) return selector.id;
    const tag = selector.tags?.[0];
    if (tag) return models.find((model) => model.tags?.includes(tag))?.id;
  }
  return undefined;
}

function findModelIdForThinkingField(
  name: string,
  models: CatalogModel[],
  formValues?: Record<string, unknown>,
): string | undefined {
  if (!formValues) return undefined;

  const keys: string[] = [];
  if (name === "thinking_level") {
    keys.push("model");
  } else if (name.endsWith(".thinking_level")) {
    const prefix = name.slice(0, -".thinking_level".length);
    keys.push(`${prefix}.model`, "model");
  }

  for (const key of keys) {
    const modelId = extractModelId(formValues[key], models);
    if (modelId) return modelId;
  }

  return undefined;
}

function preferredThinkingLevel(levels: string[]): string {
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

export function useThinkingCapability(name: string, formValues?: Record<string, unknown>): ThinkingCapability {
  const { models } = useModels();

  return useMemo(() => {
    const modelId = findModelIdForThinkingField(name, models, formValues);
    return resolveThinkingCapabilityForModel(modelId, models);
  }, [models, name, formValues]);
}

export function reconcileThinkingLevel(level: string, capability: ThinkingCapability): string {
  if (!capability.supportsThinking || capability.levels.length === 0) {
    return "";
  }
  if (level && capability.levels.includes(level)) {
    return level;
  }
  return capability.defaultLevel;
}
