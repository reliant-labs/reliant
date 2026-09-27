import { describe, expect, it } from "vitest";
import {
  autoThinkingLabel,
  reconcileThinkingLevel,
  resolveThinkingCapabilityForSelector,
} from "../useThinkingCapability";

// Provider-sorted like ListModels: the FIRST model carrying "flagship" is not
// the one the tier resolves to, which is exactly the guess tiers replace.
const models = [
  {
    id: "claude-5-sonnet@anthropic",
    tags: ["flagship", "moderate"],
    supportedThinkingLevels: ["low", "medium", "high"],
  },
  {
    id: "claude-5.5-opus@anthropic",
    tags: ["flagship", "moderate"],
    supportedThinkingLevels: ["low", "medium", "high", "xhigh"],
  },
];

const tiers = {
  flagship: { modelId: "claude-5.5-opus@anthropic", thinkingLevel: "xhigh" },
};

describe("resolveThinkingCapabilityForSelector", () => {
  it("resolves a tag selector to the tier's model and effort", () => {
    const capability = resolveThinkingCapabilityForSelector({ tags: ["flagship"] }, models, tiers);

    expect(capability.modelId).toBe("claude-5.5-opus@anthropic");
    expect(capability.levels).toEqual(["xhigh", "high", "medium", "low"]);
    expect(capability.tag).toBe("flagship");
    expect(capability.tierLevel).toBe("xhigh");
    expect(autoThinkingLabel(capability)).toBe("Auto (xhigh)");
  });

  it("falls back to the first catalog model with the tag when tiers are absent", () => {
    const capability = resolveThinkingCapabilityForSelector({ tags: ["flagship"] }, models, undefined);

    expect(capability.modelId).toBe("claude-5-sonnet@anthropic");
    expect(capability.tag).toBe("flagship");
    expect(capability.tierLevel).toBeUndefined();
    expect(autoThinkingLabel(capability)).toBe("Auto");
  });

  it("leaves explicit-id selectors untouched by tiers", () => {
    const capability = resolveThinkingCapabilityForSelector({ id: "claude-5-sonnet@anthropic" }, models, tiers);

    expect(capability.modelId).toBe("claude-5-sonnet@anthropic");
    expect(capability.tag).toBeUndefined();
    expect(capability.tierLevel).toBeUndefined();
    expect(autoThinkingLabel(capability)).toBeUndefined();
  });
});

describe("reconcileThinkingLevel", () => {
  const tagCapability = resolveThinkingCapabilityForSelector({ tags: ["flagship"] }, models, tiers);
  const idCapability = resolveThinkingCapabilityForSelector({ id: "claude-5-sonnet@anthropic" }, models, tiers);

  it("keeps an empty level empty for a tag selector (the tier decides)", () => {
    expect(reconcileThinkingLevel("", tagCapability)).toBe("");
  });

  it("clears an unsupported level to the tier for a tag selector", () => {
    expect(reconcileThinkingLevel("max", tagCapability)).toBe("");
  });

  it("keeps a supported explicit level for a tag selector", () => {
    expect(reconcileThinkingLevel("high", tagCapability)).toBe("high");
  });

  it("falls back to the model default for an explicit-id selector", () => {
    expect(reconcileThinkingLevel("", idCapability)).toBe("medium");
    expect(reconcileThinkingLevel("xhigh", idCapability)).toBe("medium");
  });
});
