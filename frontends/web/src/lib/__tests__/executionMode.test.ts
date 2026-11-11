// @vitest-environment node
import { describe, expect, it } from "vitest";
import { isExecutionMode, isSelectorHidden, selectableOptions } from "../executionMode";

describe("executionMode", () => {
  it("lists manual and plan but not auto", () => {
    expect(selectableOptions(["manual", "auto", "plan"])).toEqual(["manual", "plan"]);
  });

  it("hides the selector when auto was the only alternative to one mode", () => {
    expect(isSelectorHidden(["manual", "auto"])).toBe(true);
    expect(isSelectorHidden(["manual", "auto", "plan"])).toBe(false);
  });

  it("leaves other enums alone, even one with an auto value", () => {
    expect(isExecutionMode(["auto", "on", "off"])).toBe(false);
    expect(selectableOptions(["auto", "on", "off"])).toEqual(["auto", "on", "off"]);
    expect(isSelectorHidden(["auto", "on", "off"])).toBe(false);
    expect(isSelectorHidden(undefined)).toBe(false);
  });
});
