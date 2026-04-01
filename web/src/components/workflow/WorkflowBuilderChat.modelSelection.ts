import type { ThinkingCapability } from "../../hooks/useThinkingCapability";

interface CatalogModelLike {
  id: string;
}

export interface BuilderModelSelection {
  selectedModelId?: string;
  hasSelectedModelInCatalog: boolean;
  canResolveThinkingCapability: boolean;
}

/**
 * Resolve the effective model for WorkflowBuilderChat.
 * - Honors user-selected model when it exists in current catalog
 * - Falls back to default model when selected model is stale/missing
 */
export function resolveBuilderSelectedModel(
  selectedModelParamId: string | undefined,
  defaultModelId: string | undefined,
  models: CatalogModelLike[],
): BuilderModelSelection {
  const hasSelectedModelInCatalog = selectedModelParamId
    ? models.some((model) => model.id === selectedModelParamId)
    : false;

  const selectedModelId = hasSelectedModelInCatalog
    ? selectedModelParamId
    : defaultModelId;

  return {
    selectedModelId,
    hasSelectedModelInCatalog,
    canResolveThinkingCapability: Boolean(selectedModelId),
  };
}

/**
 * Determine whether thinking reconciliation should run.
 * We skip reconciliation when there is no resolvable model yet.
 */
export function shouldReconcileBuilderThinkingLevel(
  canResolveThinkingCapability: boolean,
): boolean {
  return canResolveThinkingCapability;
}

/**
 * Determine whether selector should be disabled.
 */
export function isThinkingSelectorDisabled(args: {
  isCatalogLoading: boolean;
  canResolveThinkingCapability: boolean;
  modelsError: string | null;
}): boolean {
  return (
    args.isCatalogLoading ||
    !args.canResolveThinkingCapability ||
    Boolean(args.modelsError)
  );
}

export type ThinkingSelectorDisplayState =
  | "loading"
  | "error"
  | "empty"
  | "ready";

export function getThinkingSelectorDisplayState(args: {
  isCatalogLoading: boolean;
  modelsError: string | null;
  canResolveThinkingCapability: boolean;
}): ThinkingSelectorDisplayState {
  if (args.isCatalogLoading) return "loading";
  if (args.modelsError) return "error";
  if (!args.canResolveThinkingCapability) return "empty";
  return "ready";
}

export function reconcileThinkingForBuilder(
  thinkingLevel: string,
  capability: ThinkingCapability,
  canResolveThinkingCapability: boolean,
  reconcileThinkingLevel: (level: string, capability: ThinkingCapability) => string,
): string {
  if (!shouldReconcileBuilderThinkingLevel(canResolveThinkingCapability)) {
    return thinkingLevel;
  }
  return reconcileThinkingLevel(thinkingLevel, capability);
}
