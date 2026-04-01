// Copyright (c) 2025 Reliant Labs

import { describe, it, expect } from "vitest";
import { flatToNestedParams, hasDefaultValue, isModelInputRequired } from "../paramUtils";
import { buildWorkflowInputsFromProto } from "../../components/Chat/ChatInput";

describe("flatToNestedParams", () => {
  it("converts flat dot-notation keys to nested structure", () => {
    const flat = {
      "agent.model": { id: "claude-3-5-sonnet-latest" },
      "agent.temperature": 0.7,
      "mode": "fast",
    };

    const nested = flatToNestedParams(flat);

    expect(nested).toEqual({
      agent: {
        model: { id: "claude-3-5-sonnet-latest" },
        temperature: 0.7,
      },
      mode: "fast",
    });
  });

  it("handles top-level params without dots", () => {
    const flat = {
      mode: "fast",
      debug: true,
    };

    const nested = flatToNestedParams(flat);

    expect(nested).toEqual({
      mode: "fast",
      debug: true,
    });
  });

  it("handles multiple groups", () => {
    const flat = {
      "agent.model": { id: "claude" },
      "agent.temperature": 0.5,
      "implementer.model": { id: "gpt-4" },
      "implementer.max_tokens": 4000,
    };

    const nested = flatToNestedParams(flat);

    expect(nested).toEqual({
      agent: {
        model: { id: "claude" },
        temperature: 0.5,
      },
      implementer: {
        model: { id: "gpt-4" },
        max_tokens: 4000,
      },
    });
  });

  it("handles empty input", () => {
    const flat = {};
    const nested = flatToNestedParams(flat);
    expect(nested).toEqual({});
  });

  it("handles already-nested values (passes through)", () => {
    // If params are already nested, they should pass through
    const alreadyNested = {
      agent: { model: { id: "claude" } },
      mode: "fast",
    };

    const result = flatToNestedParams(alreadyNested);

    // Top-level keys without dots are kept as-is
    expect(result).toEqual({
      agent: { model: { id: "claude" } },
      mode: "fast",
    });
  });
});

describe("hasDefaultValue", () => {
  it("returns true for non-model types with default", () => {
    expect(hasDefaultValue({ type: "string", default: "hello" })).toBe(true);
    expect(hasDefaultValue({ type: "number", default: 42 })).toBe(true);
    expect(hasDefaultValue({ type: "boolean", default: false })).toBe(true);
  });

  it("returns false for undefined/null defaults", () => {
    expect(hasDefaultValue({ type: "string" })).toBe(false);
    expect(hasDefaultValue({ type: "string", default: undefined })).toBe(false);
    expect(hasDefaultValue({ type: "string", default: null })).toBe(false);
  });

  it("returns false for model type with empty string default", () => {
    expect(hasDefaultValue({ type: "model", default: "" })).toBe(false);
  });

  it("returns true for model type with actual default", () => {
    expect(hasDefaultValue({ type: "model", default: { id: "claude-3-5-sonnet-latest" } })).toBe(true);
  });

  it("returns true for non-model types with empty string default", () => {
    // Empty string is a valid default for string types
    expect(hasDefaultValue({ type: "string", default: "" })).toBe(true);
  });
});

describe("isModelInputRequired", () => {
  it("returns false for non-model types", () => {
    expect(isModelInputRequired({ type: "string", default: "" })).toBe(false);
    expect(isModelInputRequired({ type: "number" })).toBe(false);
  });

  it("returns true for model with empty default", () => {
    expect(isModelInputRequired({ type: "model", default: "" })).toBe(true);
  });

  it("returns false for model with actual default", () => {
    expect(isModelInputRequired({ type: "model", default: { id: "gpt-4" } })).toBe(false);
  });
});

describe("buildWorkflowInputsFromProto", () => {
  it("maps V2 proto input config into chat toolbar/config inputs", () => {
    const { inputs } = buildWorkflowInputsFromProto({
      mode: {
        type: "enum",
        config: {
          case: "enumInput",
          value: {
            base: {
              description: "Execution mode",
              ui: "toolbar",
            },
            default: { kind: { case: "stringValue", value: "auto" } },
            enumValues: ["manual", "auto", "plan"],
            multi: false,
          },
        },
      },
      max_turns: {
        type: "integer",
        config: {
          case: "integerInput",
          value: {
            base: {
              description: "Max turns",
              ui: "config",
            },
            default: BigInt(200),
            min: BigInt(1),
            max: BigInt(500),
          },
        },
      },
    });

    // Raw proto Input objects are passed through directly
    expect(inputs.mode.type).toBe("enum");
    expect(inputs.mode.config.case).toBe("enumInput");
    expect(inputs.mode.config.value.base.ui).toBe("toolbar");
    expect(inputs.mode.config.value.enumValues).toEqual(["manual", "auto", "plan"]);
    expect(inputs.mode.config.value.default).toMatchObject({
      kind: { case: "stringValue", value: "auto" },
    });

    expect(inputs.max_turns.type).toBe("integer");
    expect(inputs.max_turns.config.case).toBe("integerInput");
    expect(inputs.max_turns.config.value.base.ui).toBe("config");
    expect(inputs.max_turns.config.value.default).toBe(BigInt(200));
    expect(inputs.max_turns.config.value.min).toBe(BigInt(1));
    expect(inputs.max_turns.config.value.max).toBe(BigInt(500));
  });

  it("flattens V2 group input configs and preserves group preset tag", () => {
    const { inputs, groupTags, groupUIs } = buildWorkflowInputsFromProto({
      agent: {
        type: "group",
        config: {
          case: "groupInput",
          value: {
            base: {
              ui: "toolbar",
            },
            presets: {
              tag: "agent",
            },
            inputs: {
              model: {
                type: "model",
                config: {
                  case: "modelInput",
                  value: {
                    base: {
                      description: "LLM model",
                      ui: "config",
                    },
                    default: {
                      tags: ["flagship"],
                    },
                  },
                },
              },
            },
          },
        },
      },
    });

    expect(groupTags.agent).toBe("agent");
    expect(groupUIs.agent).toBe("toolbar");

    // Raw proto Input object is passed through directly (not converted to flat schema)
    expect(inputs["agent.model"].type).toBe("model");
    expect(inputs["agent.model"].config.case).toBe("modelInput");
    expect(inputs["agent.model"].config.value.base.description).toBe("LLM model");
    expect(inputs["agent.model"].config.value.base.ui).toBe("config");
    expect(inputs["agent.model"].config.value.default).toMatchObject({
      tags: ["flagship"],
    });
  });
});
