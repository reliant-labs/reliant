import { describe, expect, it } from "vitest";
import {
  getThinkingSelectorDisplayState,
  isThinkingSelectorDisabled,
  reconcileThinkingForBuilder,
  resolveBuilderSelectedModel,
} from "../WorkflowBuilderChat.modelSelection";
import type { ThinkingCapability } from "../../../hooks/useThinkingCapability";

describe("WorkflowBuilderChat model selection helpers", () => {
  it("falls back to default model when selected model is stale", () => {
    const result = resolveBuilderSelectedModel(
      "stale-model@provider",
      "claude-4.6-sonnet@anthropic",
      [
        { id: "claude-4.6-sonnet@anthropic" },
        { id: "gpt-5.3-codex@codex" },
      ],
    );

    expect(result.selectedModelId).toBe("claude-4.6-sonnet@anthropic");
    expect(result.hasSelectedModelInCatalog).toBe(false);
    expect(result.canResolveThinkingCapability).toBe(true);
  });

  it("keeps selected model when it exists in catalog", () => {
    const result = resolveBuilderSelectedModel(
      "gpt-5.3-codex@codex",
      "claude-4.6-sonnet@anthropic",
      [
        { id: "claude-4.6-sonnet@anthropic" },
        { id: "gpt-5.3-codex@codex" },
      ],
    );

    expect(result.selectedModelId).toBe("gpt-5.3-codex@codex");
    expect(result.hasSelectedModelInCatalog).toBe(true);
    expect(result.canResolveThinkingCapability).toBe(true);
  });

  it("cannot resolve thinking capability when no selected/default model exists", () => {
    const result = resolveBuilderSelectedModel(undefined, undefined, []);

    expect(result.selectedModelId).toBeUndefined();
    expect(result.canResolveThinkingCapability).toBe(false);
  });
});

describe("WorkflowBuilderChat thinking selector state", () => {
  it("reports loading state", () => {
    const state = getThinkingSelectorDisplayState({
      isCatalogLoading: true,
      modelsError: null,
      canResolveThinkingCapability: false,
    });

    expect(state).toBe("loading");
  });

  it("reports error state", () => {
    const state = getThinkingSelectorDisplayState({
      isCatalogLoading: false,
      modelsError: "boom",
      canResolveThinkingCapability: true,
    });

    expect(state).toBe("error");
  });

  it("reports empty state when no resolvable model", () => {
    const state = getThinkingSelectorDisplayState({
      isCatalogLoading: false,
      modelsError: null,
      canResolveThinkingCapability: false,
    });

    expect(state).toBe("empty");
  });

  it("reports ready state", () => {
    const state = getThinkingSelectorDisplayState({
      isCatalogLoading: false,
      modelsError: null,
      canResolveThinkingCapability: true,
    });

    expect(state).toBe("ready");
  });

  it("disables selector during loading", () => {
    expect(
      isThinkingSelectorDisabled({
        isCatalogLoading: true,
        canResolveThinkingCapability: true,
        modelsError: null,
      }),
    ).toBe(true);
  });

  it("disables selector when models are unavailable", () => {
    expect(
      isThinkingSelectorDisabled({
        isCatalogLoading: false,
        canResolveThinkingCapability: false,
        modelsError: null,
      }),
    ).toBe(true);
  });

  it("enables selector when catalog is ready and capability can be resolved", () => {
    expect(
      isThinkingSelectorDisabled({
        isCatalogLoading: false,
        canResolveThinkingCapability: true,
        modelsError: null,
      }),
    ).toBe(false);
  });
});

describe("WorkflowBuilderChat thinking reconciliation gate", () => {
  const capability: ThinkingCapability = {
    modelId: "claude-4.6-sonnet@anthropic",
    supportsThinking: true,
    levels: ["low", "medium", "high"],
    defaultLevel: "medium",
  };

  it("does not reconcile when thinking capability cannot be resolved", () => {
    const reconcile = (_level: string, _capability: ThinkingCapability): string => "";

    const result = reconcileThinkingForBuilder(
      "medium",
      capability,
      false,
      reconcile,
    );

    expect(result).toBe("medium");
  });

  it("reconciles when thinking capability is resolvable", () => {
    const reconcile = (_level: string, _capability: ThinkingCapability): string => "high";

    const result = reconcileThinkingForBuilder(
      "medium",
      capability,
      true,
      reconcile,
    );

    expect(result).toBe("high");
  });
});
