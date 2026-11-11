// @vitest-environment node
/**
 * The `?tour=` grammar and the root route's retention of it.
 */
import { describe, expect, it } from "vitest";
import {
  formatTourParam,
  parseTourParam,
  pickerParam,
  retainTourParam,
} from "../tourParam";

describe("parseTourParam", () => {
  it("reads journey:step, a bare journey and a bare step id", () => {
    expect(parseTourParam("workflows:wf-new")).toEqual({ kind: "journey", journey: "workflows", stepId: "wf-new" });
    expect(parseTourParam("forge")).toEqual({ kind: "journey", journey: "forge", stepId: null });
    expect(parseTourParam("wf-new")).toEqual({ kind: "step", stepId: "wf-new" });
  });

  it("reads the picker, from onboarding or a menu", () => {
    expect(parseTourParam("pick")).toEqual({ kind: "picker", source: "onboarding" });
    expect(parseTourParam(pickerParam("menu"))).toEqual({ kind: "picker", source: "menu" });
  });

  it("treats junk as no tour, never an error", () => {
    for (const raw of [undefined, null, 42, true, "", "nope:wf-new", "Has Spaces", "a:b:c:"]) {
      const parsed = parseTourParam(raw);
      expect(parsed === null || parsed.kind === "step").toBe(true);
    }
    expect(parseTourParam("nope:wf-new")).toBeNull();
  });

  it("round-trips formatTourParam", () => {
    expect(parseTourParam(formatTourParam("forge", "fg-trace"))).toEqual({
      kind: "journey",
      journey: "forge",
      stepId: "fg-trace",
    });
  });
});

describe("retainTourParam (root search middleware)", () => {
  const run = (current: Record<string, unknown>, dest: (s: Record<string, unknown>) => Record<string, unknown>) =>
    retainTourParam({ search: current, next: dest });

  it("keeps ?tour= across a navigation that doesn't mention it", () => {
    expect(run({ tour: "workflows:wf-new", q: 1 }, () => ({}))).toEqual({ tour: "workflows:wf-new" });
  });

  it("lets a navigation set it, or clear it with tour: undefined", () => {
    expect(run({ tour: "a" }, () => ({ tour: "b" }))).toEqual({ tour: "b" });
    expect(run({ tour: "a" }, () => ({ tour: undefined }))).toEqual({ tour: undefined });
  });

  it("adds nothing when no tour is open", () => {
    expect(run({}, () => ({ x: 1 }))).toEqual({ x: 1 });
  });
});
