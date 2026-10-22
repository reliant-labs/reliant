import { describe, expect, it } from "vitest";
import { ProviderPolicy_OnLimit } from "../../gen/reliant/v1/chat_pb";
import {
  choiceFromDefault,
  choiceFromPolicy,
  fallbackSuggestions,
  parsePolicyDefaults,
  policyFromChoice,
  primaryDriverOfSelection,
  withDefault,
  type PolicyModel,
} from "../providerPolicy";

const opus: PolicyModel = { id: "claude-5.5-opus@claude", name: "Claude Opus", provider: "Claude subscription", driverId: "claude", tags: ["flagship"] };
const sol: PolicyModel = { id: "gpt-5.6-sol@codex", name: "GPT-5.6 Sol", provider: "Codex", driverId: "codex", tags: ["flagship"] };
const solApi: PolicyModel = { id: "gpt-5.6-sol@openai", name: "GPT-5.6 Sol", provider: "OpenAI", driverId: "openai", tags: ["flagship"] };
const terra: PolicyModel = { id: "gpt-5.6-terra@codex", name: "GPT-5.6 Terra", provider: "Codex", driverId: "codex", tags: ["moderate"] };
const qwen: PolicyModel = { id: "qwen3@local", name: "qwen3", provider: "Local", driverId: "local", local: { daemonId: "d" } };

describe("fallbackSuggestions", () => {
  it("offers one stand-in per OTHER connected provider, never the primary or a local model", () => {
    const got = fallbackSuggestions([opus, sol, solApi, qwen], "claude", opus);
    expect(got.map((m) => m.id).sort()).toEqual(["gpt-5.6-sol@codex", "gpt-5.6-sol@openai"]);
  });

  it("prefers the same model, then the same tier, then anything", () => {
    expect(fallbackSuggestions([terra, sol], "claude", opus)[0].id).toBe("gpt-5.6-sol@codex");
    expect(fallbackSuggestions([terra], "claude", opus)[0].id).toBe("gpt-5.6-terra@codex");
    expect(fallbackSuggestions([terra], "claude", null)[0].id).toBe("gpt-5.6-terra@codex");
  });

  it("offers nothing with a single provider", () => {
    expect(fallbackSuggestions([opus], "claude", opus)).toEqual([]);
  });
});

describe("primaryDriverOfSelection", () => {
  const tiers = { flagship: { modelId: "claude-5.5-opus@claude" } };
  it("reads a pinned id, a providers list, and a tier", () => {
    expect(primaryDriverOfSelection({ id: "gpt-5.6-sol@codex" }, tiers)).toBe("codex");
    expect(primaryDriverOfSelection({ tags: ["x"], providers: ["openai"] }, tiers)).toBe("openai");
    expect(primaryDriverOfSelection({ tags: ["flagship"] }, tiers)).toBe("claude");
    expect(primaryDriverOfSelection(undefined, tiers)).toBe("");
  });
});

describe("choices", () => {
  it("round-trips a policy message", () => {
    for (const choice of [{ kind: "inherit" }, { kind: "wait" }, { kind: "fallback", model: "gpt-5.6-sol@codex" }] as const) {
      expect(choiceFromPolicy(policyFromChoice(choice))).toEqual(choice);
    }
    expect(policyFromChoice({ kind: "inherit" }).onLimit).toBe(ProviderPolicy_OnLimit.UNSPECIFIED);
  });

  it("a fallback with no model is a wait", () => {
    expect(choiceFromPolicy({ onLimit: ProviderPolicy_OnLimit.FALLBACK })).toEqual({ kind: "wait" });
  });
});

describe("defaults", () => {
  it("parse leniently and default to wait", () => {
    expect(parsePolicyDefaults(undefined)).toEqual({});
    expect(parsePolicyDefaults("nope")).toEqual({});
    expect(parsePolicyDefaults("[1]")).toEqual({});
    const d = parsePolicyDefaults('{"claude":{"on_limit":"fallback","fallback_model":"gpt-5.6-sol@codex"}}');
    expect(choiceFromDefault(d, "claude")).toEqual({ kind: "fallback", model: "gpt-5.6-sol@codex" });
    expect(choiceFromDefault(d, "codex")).toEqual({ kind: "wait" });
  });

  it("withDefault writes the server's shape and does not mutate", () => {
    const before = {};
    const next = withDefault(before, "claude", { kind: "fallback", model: "gpt-5.6-sol@codex" });
    expect(next).toEqual({ claude: { on_limit: "fallback", fallback_model: "gpt-5.6-sol@codex" } });
    expect(before).toEqual({});
    expect(withDefault(next, "claude", { kind: "wait" })).toEqual({ claude: { on_limit: "wait" } });
    expect(withDefault(next, "claude", { kind: "inherit" })).toEqual({});
  });
});
