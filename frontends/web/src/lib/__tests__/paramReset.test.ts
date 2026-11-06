import { describe, expect, it } from "vitest";
import { adoptServerParams, applyParamPatch, nestChangedParams, unsyncedParams } from "../paramUtils";

describe("parameter reset patches (root and thread)", () => {
  it.each([
    [{}, { mode: null }],
    [{ temperature: 0.5 }, { mode: null, temperature: 0.5 }],
  ])("carries a removed override alongside remaining edits: %j", (local, expected) => {
    expect(unsyncedParams(local, { mode: "plan" })).toEqual(expected);
  });

  it("acknowledges deletion without retaining a tombstone or resending it", () => {
    const baseline = applyParamPatch({ mode: "plan", tools: ["read"] }, { mode: null });
    expect(baseline).toEqual({ tools: ["read"] });
    expect(unsyncedParams({ tools: ["read"] }, baseline)).toEqual({});
  });

  it("preserves a reset through server refresh before acknowledgement", () => {
    const adopted = adoptServerParams({}, { mode: "plan" }, { mode: "manual" });
    expect(adopted.params).toEqual({});
    expect(unsyncedParams(adopted.params, adopted.synced)).toEqual({ mode: null });
  });

  it("preserves a newer edit while the reset acknowledgement converges", () => {
    const baseline = applyParamPatch({ mode: "plan" }, { mode: null });
    const adopted = adoptServerParams({ mode: "manual" }, baseline, {});
    expect(adopted.params).toEqual({ mode: "manual" });
    expect(unsyncedParams(adopted.params, adopted.synced)).toEqual({ mode: "manual" });
  });

  it.each([
    [{ "agent.tools": ["read"] }, { agent: { tools: ["read"] } }],
    [{}, { agent: null }],
  ])("replaces reset groups whole, including removing the last key: %j", (local, expected) => {
    const before = { "agent.mode": "plan", "agent.tools": ["read"] };
    const changed = unsyncedParams(local, before);
    expect(nestChangedParams(changed, local, new Set(["agent"]))).toEqual(expected);
    const acknowledged = applyParamPatch(before, changed);
    expect(acknowledged).toEqual(local);
    expect(unsyncedParams(local, acknowledged)).toEqual({});
  });
});
