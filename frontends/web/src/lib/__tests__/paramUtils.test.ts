// Copyright (c) 2025 Reliant Labs

import { describe, it, expect } from "vitest";
import {
  adoptServerParams,
  flatToNestedParams,
  groupNamesOf,
  hasDefaultValue,
  isModelInputRequired,
  nestChangedParams,
  nestedToFlatParams,
  paramValuesEqual,
  currentThinkingLevel,
  unsyncedParams,
  repairLegacyPresetSelections,
  repairLegacyWorkflowParams,
  threadParamsAcceptChanges,
  withThinkingLevel,
} from "../paramUtils";
import { buildWorkflowInputsFromProto } from "../../components/Chat/ChatInput";

// configValue is an input's active config oneof value, read loosely: each
// assertion names fields of one specific case, which the union cannot.
function configValue(input: { config?: { value?: unknown } } | undefined): any {
  return input?.config?.value ?? {};
}


const GROUPS = new Set(["agent", "implementer"]);

describe("flatToNestedParams", () => {
  it("nests a known group's flat keys", () => {
    expect(
      flatToNestedParams(
        {
          "agent.model": { id: "claude-3-5-sonnet-latest" },
          "agent.temperature": 0.7,
          mode: "fast",
        },
        GROUPS
      )
    ).toEqual({
      agent: { model: { id: "claude-3-5-sonnet-latest" }, temperature: 0.7 },
      mode: "fast",
    });
  });

  it("handles several groups, and empty input", () => {
    expect(
      flatToNestedParams(
        { "agent.model": { id: "claude" }, "implementer.model": { id: "gpt-4" }, "implementer.max_tokens": 4000 },
        GROUPS
      )
    ).toEqual({
      agent: { model: { id: "claude" } },
      implementer: { model: { id: "gpt-4" }, max_tokens: 4000 },
    });
    expect(flatToNestedParams({}, GROUPS)).toEqual({});
  });

  it("leaves a dotted key that is not under a known group alone, for the send boundary to refuse", () => {
    expect(flatToNestedParams({ "default.model": "x", "agent.model": "y" }, GROUPS)).toEqual({
      "default.model": "x",
      agent: { model: "y" },
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
    expect(inputs.mode.config?.case).toBe("enumInput");
    expect(configValue(inputs.mode).base.ui).toBe("toolbar");
    expect(configValue(inputs.mode).enumValues).toEqual(["manual", "auto", "plan"]);
    expect(configValue(inputs.mode).default).toMatchObject({
      kind: { case: "stringValue", value: "auto" },
    });

    expect(inputs.max_turns.type).toBe("integer");
    expect(inputs.max_turns.config?.case).toBe("integerInput");
    expect(configValue(inputs.max_turns).base.ui).toBe("config");
    expect(configValue(inputs.max_turns).default).toBe(BigInt(200));
    expect(configValue(inputs.max_turns).min).toBe(BigInt(1));
    expect(configValue(inputs.max_turns).max).toBe(BigInt(500));
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
    expect(inputs["agent.model"].config?.case).toBe("modelInput");
    expect(configValue(inputs["agent.model"]).base.description).toBe("LLM model");
    expect(configValue(inputs["agent.model"]).base.ui).toBe("config");
    expect(configValue(inputs["agent.model"]).default).toMatchObject({
      tags: ["flagship"],
    });
  });
});

describe("nestedToFlatParams", () => {
  it("flattens a schema group into flat keys", () => {
    expect(
      nestedToFlatParams({ agent: { model: { id: "claude-5-sonnet" }, temperature: 0.7 }, mode: "fast" }, GROUPS)
    ).toEqual({
      "agent.model": { id: "claude-5-sonnet" },
      "agent.temperature": 0.7,
      mode: "fast",
    });
  });

  it("round-trips with flatToNestedParams", () => {
    const flat = { "agent.model": { tags: ["moderate"] }, "agent.thinking_level": "high", mode: "fast" };
    expect(nestedToFlatParams(flatToNestedParams(flat, GROUPS), GROUPS)).toEqual(flat);
  });

  it("keeps a top-level model selector whole: it is a value, not a group", () => {
    // The bug this guards: {model: {id}} flattened to "model.id", a key no input
    // is named, so the composer could never adopt the model a run was using.
    expect(
      nestedToFlatParams({ mode: "auto", model: { id: "claude-5.5-opus@claude", thinking_level: "low" } }, new Set())
    ).toEqual({ mode: "auto", model: { id: "claude-5.5-opus@claude", thinking_level: "low" } });
  });

  it("leaves arrays as whole values rather than flattening them", () => {
    expect(nestedToFlatParams({ skills: ["a", "b"] }, GROUPS)).toEqual({ skills: ["a", "b"] });
  });
});

describe("groupNamesOf", () => {
  it("reads the group names out of the flattened input schema", () => {
    expect(
      groupNamesOf({ mode: {}, model: {}, "Agent A.model": {}, "Agent A.tools": {}, "Agent B.model": {} })
    ).toEqual(new Set(["Agent A", "Agent B"]));
    expect(groupNamesOf(null)).toEqual(new Set());
  });
});

describe("nestChangedParams", () => {
  it("sends a changed group whole, so the run and the selection do not lose its other params", () => {
    const all = { mode: "plan", "agent.model": { id: "b" }, "agent.temperature": 0.7, "implementer.model": { id: "z" } };
    expect(nestChangedParams({ "agent.model": { id: "b" } }, all, GROUPS)).toEqual({
      agent: { model: { id: "b" }, temperature: 0.7 },
    });
  });

  it("sends top-level keys as they are", () => {
    expect(nestChangedParams({ mode: "plan" }, { mode: "plan", "agent.model": { id: "a" } }, GROUPS)).toEqual({
      mode: "plan",
    });
  });
});

describe("unsyncedParams", () => {
  it("is the keys that differ from the baseline, by value", () => {
    expect(
      unsyncedParams(
        { mode: "plan", model: { id: "a" }, tools: ["x"] },
        { mode: "auto", model: { id: "a" } }
      )
    ).toEqual({ mode: "plan", tools: ["x"] });
  });
});

describe("paramValuesEqual", () => {
  it("treats structurally equal model selectors as equal", () => {
    // The server round-trip returns an equal-but-distinct object; reference
    // equality would report a phantom unsaved edit.
    expect(
      paramValuesEqual({ tags: ["moderate"] }, { tags: ["moderate"] })
    ).toBe(true);
  });

  it("distinguishes different model selectors", () => {
    expect(
      paramValuesEqual({ tags: ["moderate"] }, { tags: ["flagship"] })
    ).toBe(false);
    expect(
      paramValuesEqual({ id: "claude-5-sonnet" }, { id: "claude-5-opus" })
    ).toBe(false);
  });

  it("compares primitives and mismatched shapes", () => {
    expect(paramValuesEqual("high", "high")).toBe(true);
    expect(paramValuesEqual("high", "low")).toBe(false);
    expect(paramValuesEqual(undefined, undefined)).toBe(true);
    expect(paramValuesEqual({ id: "x" }, undefined)).toBe(false);
    expect(paramValuesEqual({ id: "x" }, { id: "x", extra: 1 })).toBe(false);
  });
});

describe("adoptServerParams", () => {
  it("takes the server's value for a key the user has not edited", () => {
    // The chat's pick was made on another device, or the server moved the model
    // off a provider that cannot serve it: the pill must show what runs.
    const { params, synced } = adoptServerParams(
      { model: { id: "gpt-5.6-sol@codex" } },
      { model: { id: "gpt-5.6-sol@codex" } },
      { model: { id: "claude-5.5-opus@claude" } }
    );
    expect(params.model).toEqual({ id: "claude-5.5-opus@claude" });
    expect(synced.model).toEqual({ id: "claude-5.5-opus@claude" });
  });

  it("keeps an edit not yet pushed, and its baseline, so the next push carries it", () => {
    const { params, synced } = adoptServerParams(
      { mode: "plan" }, // typed here
      { mode: "auto" }, // last known on the server
      { mode: "manual" } // changed elsewhere meanwhile
    );
    expect(params.mode).toBe("plan");
    expect(synced.mode).toBe("auto");
    expect(unsyncedParams(params, synced)).toEqual({ mode: "plan" });
  });

  it("adopts keys only the server has, and drops ones it no longer holds", () => {
    const { params } = adoptServerParams(
      { stale: 1, mode: "auto" },
      { stale: 1, mode: "auto" },
      { mode: "auto", tools: ["read"] }
    );
    expect(params).toEqual({ mode: "auto", tools: ["read"] });
  });

  it("opening a chat (empty baseline, empty draft) shows exactly the server and pushes nothing", () => {
    const server = { model: { id: "a" }, mode: "plan" };
    const { params, synced } = adoptServerParams({}, {}, server);
    expect(params).toEqual(server);
    expect(unsyncedParams(params, synced)).toEqual({});
  });

  it("treats a value-equal server object as unchanged, not as a pending edit", () => {
    const { params, synced } = adoptServerParams(
      { model: { id: "a" } },
      { model: { id: "a" } },
      { model: { id: "a" } }
    );
    expect(unsyncedParams(params, synced)).toEqual({});
  });
});

describe("threadParamsAcceptChanges", () => {
  it("accepts edits for a running thread", () => {
    expect(threadParamsAcceptChanges({ isRunning: true, resumable: false })).toBe(true);
  });

  // A sub-agent that hit a provider usage limit is switched to another
  // provider's model BEFORE the message that resumes it, so the picker on its
  // thread has to keep working while it is stopped.
  it("accepts edits for a sub-agent that stopped on an error", () => {
    expect(threadParamsAcceptChanges({ isRunning: false, resumable: true })).toBe(true);
  });

  it("refuses edits for a finished thread, and when nothing is known yet", () => {
    expect(threadParamsAcceptChanges({ isRunning: false, resumable: false })).toBe(false);
    expect(threadParamsAcceptChanges(null)).toBe(false);
  });
});

describe("thinking level lives on the model selector (Cmd+K H)", () => {
  it("withThinkingLevel writes model.thinking_level and never a top-level key", () => {
    const next = withThinkingLevel({ mode: "auto", model: { tags: ["flagship"] } }, "high");
    expect(next).toEqual({ mode: "auto", model: { tags: ["flagship"], thinking_level: "high" } });
    expect(next).not.toHaveProperty("thinking_level");
  });

  it("promotes a bare-string model to a selector", () => {
    expect(withThinkingLevel({ model: "gpt-5" }, "low")).toEqual({
      model: { id: "gpt-5", thinking_level: "low" },
    });
  });

  it("leaves params without a model alone rather than inventing a top-level key", () => {
    const params = { mode: "auto" };
    expect(withThinkingLevel(params, "high")).toBe(params);
  });

  it("currentThinkingLevel reads it back from the selector", () => {
    expect(currentThinkingLevel({ model: { id: "m", thinking_level: "medium" } })).toBe("medium");
    expect(currentThinkingLevel({ thinking_level: "medium" })).toBeUndefined();
    expect(currentThinkingLevel({})).toBeUndefined();
  });
});

describe("repairLegacyWorkflowParams", () => {
  it("returns the same object when nothing needs repair", () => {
    const params = { mode: "auto", model: { id: "m" }, "Agent A.model": { id: "x" } };
    expect(repairLegacyWorkflowParams(params)).toBe(params);
  });

  it("moves a stored top-level thinking_level onto the model selector", () => {
    expect(
      repairLegacyWorkflowParams({ model: { tags: ["fast"] }, thinking_level: "high" })
    ).toEqual({ model: { tags: ["fast"], thinking_level: "high" } });
  });

  it("keeps a thinking_level already on the selector and drops the stale one", () => {
    expect(
      repairLegacyWorkflowParams({ model: { id: "m", thinking_level: "low" }, thinking_level: "high" })
    ).toEqual({ model: { id: "m", thinking_level: "low" } });
  });

  it("drops a stored thinking_level when there is no selector to carry it", () => {
    expect(repairLegacyWorkflowParams({ mode: "auto", thinking_level: "high" })).toEqual({ mode: "auto" });
  });

  it("rewrites default.* keys to workflow-level keys without clobbering a set value", () => {
    expect(
      repairLegacyWorkflowParams({ "default.model": { id: "preset-model" }, "default.tools": ["a"], mode: "plan" })
    ).toEqual({ model: { id: "preset-model" }, tools: ["a"], mode: "plan" });
    expect(
      repairLegacyWorkflowParams({ model: { id: "current" }, "default.model": { id: "stale" } })
    ).toEqual({ model: { id: "current" } });
  });

  it("leaves real group keys alone", () => {
    const params = { "Agent A.model": { id: "x" }, "default.mode": "plan" };
    expect(repairLegacyWorkflowParams(params)).toEqual({ "Agent A.model": { id: "x" }, mode: "plan" });
  });
});

describe("repairLegacyPresetSelections", () => {
  it("moves the legacy default target to the workflow target", () => {
    expect(repairLegacyPresetSelections({ default: "fast-preset" })).toEqual({ "": "fast-preset" });
  });

  it("keeps an existing workflow-level selection", () => {
    expect(repairLegacyPresetSelections({ default: "old", "": "new", Agent: "a" })).toEqual({
      "": "new",
      Agent: "a",
    });
  });

  it("returns the same object when there is no legacy target", () => {
    const selections = { "": "p" };
    expect(repairLegacyPresetSelections(selections)).toBe(selections);
  });
});
