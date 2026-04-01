import { useMemo } from "react";
import { useModels } from "../store/globalDataStore";

interface CatalogModel {
  id: string;
  supportedThinkingLevels?: string[];
}

const THINKING_ORDER = ["xhigh", "high", "medium", "low"] as const;

export interface ThinkingCapability {
  modelId?: string;
  supportsThinking: boolean;
  levels: string[];
  defaultLevel: string;
}

function extractModelId(candidate: unknown): string | undefined {
  if (typeof candidate === "string") {
    return candidate || undefined;
  }
  if (typeof candidate === "object" && candidate !== null) {
    return (candidate as { id?: string }).id;
  }
  return undefined;
}

function findModelIdForThinkingField(name: string, formValues?: Record<string, unknown>): string | undefined {
  if (!formValues) return undefined;

  const keys: string[] = [];
  if (name === "thinking_level") {
    keys.push("model");
  } else if (name.endsWith(".thinking_level")) {
    const prefix = name.slice(0, -".thinking_level".length);
    keys.push(`${prefix}.model`, "model");
  }

  for (const key of keys) {
    const modelId = extractModelId(formValues[key]);
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

  const model = models.find((m) => m.id === modelId);
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
    const modelId = findModelIdForThinkingField(name, formValues);
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
