// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { InputDef } from "@/lib/inputHelpers";
import {
  coerceInputValue,
  flattenParams,
  isRequiredInput,
  missingRequiredInputs,
  nestParams,
  presetFlatValues,
  toDisplayValue,
} from "../runWorkflowValues";

function input(type: string, configCase: string, config: Record<string, unknown> = {}): InputDef {
  return { type, config: { case: configCase, value: config } } as unknown as InputDef;
}

describe("flattenParams / nestParams", () => {
  const groups = new Set(["review"]);

  it("round-trips group params, and leaves a top-level object value intact", () => {
    const nested = {
      depth: 3,
      model: { id: "claude" },
      review: { strictness: "high", rounds: 2 },
    };
    const flat = flattenParams(nested, groups);
    expect(flat).toEqual({
      depth: 3,
      model: { id: "claude" },
      "review.strictness": "high",
      "review.rounds": 2,
    });
    expect(nestParams(flat, groups)).toEqual(nested);
  });

  it("does not invent a group from a dotted key it does not declare", () => {
    expect(nestParams({ "a.b": 1 }, groups)).toEqual({ "a.b": 1 });
  });
});

describe("presetFlatValues", () => {
  it("prefixes group presets and normalizes a bare model id", () => {
    const presets = [
      { name: "fast", description: "", source: "builtin" as const, params: { model: "haiku", depth: 1 } },
      { name: "strict", description: "", source: "project" as const, params: { strictness: "high" } },
    ];
    expect(presetFlatValues({ default: "fast", review: "strict" }, presets)).toEqual({
      model: { id: "haiku" },
      depth: 1,
      "review.strictness": "high",
    });
  });
});

describe("required inputs", () => {
  it("mirrors the server: no default, or a model default that selects nothing", () => {
    expect(isRequiredInput(input("string", "stringInput"))).toBe(true);
    expect(isRequiredInput(input("string", "stringInput", { default: "x" }))).toBe(false);
    expect(isRequiredInput(input("integer", "integerInput", { default: 0n }))).toBe(false);
    expect(isRequiredInput(input("model", "modelInput", { default: {} }))).toBe(true);
    expect(isRequiredInput(input("model", "modelInput", { default: { tags: ["flagship"] } }))).toBe(false);
  });

  it("counts a value from a preset or an override, and an empty model as unset", () => {
    const groups = [
      {
        name: "",
        label: "Parameters",
        inputs: [
          { name: "label", schema: input("string", "stringInput") },
          { name: "model", schema: input("model", "modelInput") },
        ],
      },
    ];
    expect(missingRequiredInputs(groups, {}).map((i) => i.name)).toEqual(["label", "model"]);
    expect(missingRequiredInputs(groups, { label: "bug", model: { id: "" } }).map((i) => i.name)).toEqual([
      "model",
    ]);
    expect(missingRequiredInputs(groups, { label: "bug", model: { id: "claude" } })).toEqual([]);
  });
});

describe("coerceInputValue / toDisplayValue", () => {
  it("gives the renderer's strings their real types back", () => {
    expect(coerceInputValue(input("model", "modelInput"), "claude")).toEqual({ id: "claude" });
    expect(coerceInputValue(input("model", "modelInput"), "")).toBeUndefined();
    expect(coerceInputValue(input("tools", "toolsInput"), "read, write")).toEqual(["read", "write"]);
    expect(coerceInputValue(input("tools", "toolsInput"), "")).toEqual([]);
    expect(coerceInputValue(input("array", "arrayInput"), '["a", 1]')).toEqual(["a", 1]);
    expect(coerceInputValue(input("array", "arrayInput"), "a, b")).toEqual(["a", "b"]);
    expect(coerceInputValue(input("object", "objectInput"), '{"k": 1}')).toEqual({ k: 1 });
    expect(coerceInputValue(input("string", "stringInput"), "")).toBeUndefined();
    expect(coerceInputValue(input("integer", "integerInput"), 4)).toBe(4);
  });

  it("shows lists and objects as the widgets display them", () => {
    expect(toDisplayValue(input("tools", "toolsInput"), ["read", "write"])).toBe("read, write");
    expect(toDisplayValue(input("object", "objectInput"), { k: 1 })).toBe('{"k":1}');
    expect(toDisplayValue(input("integer", "integerInput"), 3)).toBe(3);
  });
});
