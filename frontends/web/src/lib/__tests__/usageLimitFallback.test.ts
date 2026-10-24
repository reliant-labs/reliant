import { describe, expect, it } from "vitest";
import type { ProviderAccount } from "../../types/providerAccount";
import { earliestResettingAccount, selectedModelPick, usageLimitFallbackOf } from "../usageLimitFallback";

const models = [
  { id: "claude-5.5-opus@claude", name: "Claude 5.5 Opus", provider: "Claude subscription", driverId: "claude", tags: ["flagship"] },
  { id: "gpt-5.6-sol@codex", name: "GPT-5.6 Sol", provider: "Codex", driverId: "codex", tags: ["flagship"] },
  { id: "qwen@local", name: "Qwen", provider: "Local", driverId: "local", tags: ["flagship"] },
];

describe("selectedModelPick", () => {
  it("reads a pin, a tag, or a grouped model", () => {
    expect(selectedModelPick({ model: { id: "gpt-5.6-sol@codex" } })).toEqual({ id: "gpt-5.6-sol@codex" });
    expect(selectedModelPick({ model: { tags: ["flagship"] } })).toEqual({ tag: "flagship" });
    expect(selectedModelPick({ agent: { model: { id: "x@y" } } })).toEqual({ id: "x@y" });
    expect(selectedModelPick(undefined)).toBeUndefined();
  });
});

describe("usageLimitFallbackOf", () => {
  it("offers the flagship of another connected provider for a tag", () => {
    const out = usageLimitFallbackOf({
      params: { model: { tags: ["flagship"] } },
      models,
      tiers: { flagship: { modelId: "claude-5.5-opus@claude" } },
    });
    expect(out.limitedDriver).toBe("claude");
    expect(out.offer).toEqual({ modelAtDriver: "gpt-5.6-sol@codex", label: "GPT-5.6 Sol · Codex" });
  });

  it("prefers the chat's own fallback model", () => {
    const extra = [...models, { id: "gemini-3@gemini", name: "Gemini 3", provider: "Google AI", driverId: "gemini", tags: [] }];
    const out = usageLimitFallbackOf({
      params: { model: { id: "claude-5.5-opus@claude" } },
      policyFallbackModel: "gemini-3@gemini",
      models: extra,
    });
    expect(out.offer?.modelAtDriver).toBe("gemini-3@gemini");
  });

  it("offers nothing when only one hosted provider is connected", () => {
    const out = usageLimitFallbackOf({
      params: { model: { id: "claude-5.5-opus@claude" } },
      models: models.slice(0, 1),
    });
    expect(out.limitedDriver).toBe("claude");
    expect(out.offer).toBeUndefined();
  });

  it("offers nothing when no model of another provider matches the chat's model or tier", () => {
    const out = usageLimitFallbackOf({
      params: { model: { id: "claude-5.5-opus@claude" } },
      models: [
        { id: "claude-5.5-opus@claude", name: "Claude 5.5 Opus", provider: "Claude", driverId: "claude", tags: ["flagship"] },
        { id: "gpt-mini@codex", name: "GPT Mini", provider: "Codex", driverId: "codex", tags: ["fast"] },
      ],
    });
    expect(out.offer).toBeUndefined();
  });

  it("never offers a local model", () => {
    const out = usageLimitFallbackOf({ params: { model: { id: "claude-5.5-opus@claude" } }, models: [models[0], models[2]] });
    expect(out.offer).toBeUndefined();
  });

  it("does not guess when the chat's model is unknown", () => {
    expect(usageLimitFallbackOf({ params: undefined, models })).toEqual({});
  });
});

describe("earliestResettingAccount", () => {
  const NOW = Date.parse("2026-10-18T12:00:00Z");
  const at = (min: number) => new Date(NOW + min * 60_000).toISOString();
  const a = (id: string, limitedUntil?: string, provider = "claude"): ProviderAccount =>
    ({ id, provider, label: `${id}@x`, status: limitedUntil ? "limited" : "available", limitedUntil, windows: [] }) as unknown as ProviderAccount;

  it("names the account that returns first", () => {
    const got = earliestResettingAccount([a("a", at(90)), a("b", at(30)), a("c", at(10), "codex")], "claude", NOW);
    expect(got?.account.id).toBe("b");
  });
  it("says nothing for a single account", () => {
    expect(earliestResettingAccount([a("a", at(90))], "claude", NOW)).toBeUndefined();
  });
});
