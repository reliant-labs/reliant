// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { ModelSelectorSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import { formatInputDefault, formatModelSelector } from "../inputDefaultDisplay";

describe("formatInputDefault", () => {
  it("never shows proto JSON for a model selector message", () => {
    // Exactly what the detail page used to render as
    // {"$typeName":"reliant.v1.ModelSelector","id":"","tags":["flagship"],"providers":[]}.
    const selector = create(ModelSelectorSchema, { tags: ["flagship"] });
    const text = formatInputDefault(selector);
    expect(text).toBe("flagship (any provider)");
    expect(text).not.toContain("$typeName");
    expect(text).not.toContain("{");
  });

  it("names an exact model, and the providers it is pinned to", () => {
    expect(formatInputDefault(create(ModelSelectorSchema, { id: "claude-sonnet-4" }))).toBe("claude-sonnet-4");
    expect(formatInputDefault(create(ModelSelectorSchema, { tags: ["fast", "cheap"], providers: ["anthropic", "openai"] }))).toBe(
      "fast, cheap via anthropic, openai",
    );
    expect(formatModelSelector({})).toBe("Any model");
  });

  it("reads a plain-object model selector (as YAML spells it) the same way", () => {
    expect(formatInputDefault({ tags: ["flagship"] })).toBe("flagship (any provider)");
  });

  it("formats scalars in words", () => {
    expect(formatInputDefault(true)).toBe("On");
    expect(formatInputDefault(false)).toBe("Off");
    expect(formatInputDefault(3)).toBe("3");
    expect(formatInputDefault(".")).toBe(".");
    expect(formatInputDefault("")).toBe('""');
  });

  it("returns undefined when there is nothing to show", () => {
    expect(formatInputDefault(undefined)).toBeUndefined();
    expect(formatInputDefault(null)).toBeUndefined();
    expect(formatInputDefault([])).toBeUndefined();
    expect(formatInputDefault({ $typeName: "x.Y", id: "", tags: [] })).toBeUndefined();
  });

  it("joins arrays and unwraps single-field wrappers", () => {
    expect(formatInputDefault(["read", "write"])).toBe("read, write");
    expect(formatInputDefault({ text: "Summarise the diff" })).toBe("Summarise the diff");
  });

  it("drops protobuf bookkeeping from an unknown message", () => {
    const text = formatInputDefault({ $typeName: "reliant.v1.Thing", depth: 2, mode: "fast", extra: [] });
    expect(text).toBe("depth: 2 · mode: fast");
  });
});
