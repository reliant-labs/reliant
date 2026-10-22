// Copyright (c) 2025 Reliant Labs

import { describe, it, expect } from "vitest";
import { create, fromJson, toJson, type MessageInitShape } from "@bufbuild/protobuf";
import { InputSchema } from "../../gen/reliant/v1/workflow_v2_pb";
import {
  type InputDef,
  getInputIntegerDefault,
  getInputDescription,
  getInputUI,
  getInputDefault,
  getInputEnumValues,
  getInputMulti,
  getInputMin,
  getInputMax,
  getInputProperties,
  getInputRequired,
  getInputAdditionalProperties,
  getInputNestedInputs,
  getInputPresetConfig,
  getInputTags,
  getInputTag,
  getInputPattern,
  getInputMinLength,
  getInputMaxLength,
  isConfigurableInput,
  setInputDescription,
  setInputUI,
  setInputDefault,
  setInputEnumValues,
  setInputMulti,
  setInputMin,
  setInputMax,
  createInput,
  getInputExample,
  setInputExample,
  applyInputUpdates,
  changeInputType,
  isInternalInput,
} from "../inputHelpers";

// ---------------------------------------------------------------------------
// Getters
// ---------------------------------------------------------------------------

describe("getters — proto Input with config oneof", () => {
  const enumInput = {
    type: "enum",
    config: {
      case: "enumInput",
      value: {
        base: { description: "Execution mode", ui: "toolbar" },
        default: { kind: { case: "stringValue", value: "auto" } },
        enumValues: ["manual", "auto", "plan"],
        multi: false,
      },
    },
  };

  it("getInputDescription reads from base", () => {
    expect(getInputDescription(enumInput)).toBe("Execution mode");
  });

  it("getInputUI reads from base", () => {
    expect(getInputUI(enumInput)).toBe("toolbar");
  });

  it("getInputDefault unwraps proto Value", () => {
    expect(getInputDefault(enumInput)).toBe("auto");
  });

  it("getInputEnumValues", () => {
    expect(getInputEnumValues(enumInput)).toEqual(["manual", "auto", "plan"]);
  });

  it("getInputMulti", () => {
    expect(getInputMulti(enumInput)).toBe(false);
  });
});

describe("getters — integer input with bigint", () => {
  const intInput = {
    type: "integer",
    config: {
      case: "integerInput",
      value: {
        base: { description: "Max turns", ui: "config" },
        default: BigInt(200),
        min: BigInt(1),
        max: BigInt(500),
      },
    },
  };

  it("getInputDefault converts bigint to number for integer inputs", () => {
    expect(getInputDefault(intInput)).toBe(200);
  });

  it("getInputMin converts bigint to number", () => {
    expect(getInputMin(intInput)).toBe(1);
  });

  it("getInputMax converts bigint to number", () => {
    expect(getInputMax(intInput)).toBe(500);
  });
});

describe("getters — string input", () => {
  const strInput = {
    type: "string",
    config: {
      case: "stringInput",
      value: {
        base: { description: "A string", ui: "" },
        default: "hello",
        pattern: "^[a-z]+$",
        minLength: 1,
        maxLength: 100,
      },
    },
  };

  it("getInputMin returns minLength for string", () => {
    expect(getInputMin(strInput)).toBe(1);
  });

  it("getInputMax returns maxLength for string", () => {
    expect(getInputMax(strInput)).toBe(100);
  });

  it("getInputPattern", () => {
    expect(getInputPattern(strInput)).toBe("^[a-z]+$");
  });

  it("getInputMinLength", () => {
    expect(getInputMinLength(strInput)).toBe(1);
  });

  it("getInputMaxLength", () => {
    expect(getInputMaxLength(strInput)).toBe(100);
  });
});

describe("getters — object input", () => {
  const objInput = {
    type: "object",
    config: {
      case: "objectInput",
      value: {
        base: { description: "Config", ui: "" },
        properties: { name: { type: "string", description: "Name" } },
        required: ["name"],
        additionalProperties: true,
      },
    },
  };

  it("getInputProperties", () => {
    expect(getInputProperties(objInput)).toEqual({
      name: { type: "string", description: "Name" },
    });
  });

  it("getInputRequired", () => {
    expect(getInputRequired(objInput)).toEqual(["name"]);
  });

  it("getInputAdditionalProperties", () => {
    expect(getInputAdditionalProperties(objInput)).toBe(true);
  });
});

describe("getters — group input", () => {
  const groupInput = {
    type: "group",
    config: {
      case: "groupInput",
      value: {
        base: { description: "Agent config", ui: "toolbar" },
        presets: { tag: "agent" },
        inputs: {
          model: { type: "model", config: { case: "modelInput", value: { base: { description: "Model", ui: "config" } } } },
        },
      },
    },
  };

  it("getInputNestedInputs", () => {
    const nested = getInputNestedInputs(groupInput);
    expect(nested).toBeDefined();
    expect(nested!.model).toBeDefined();
  });

  it("getInputPresetConfig", () => {
    expect(getInputPresetConfig(groupInput)).toEqual({ tag: "agent" });
  });
});

describe("getters — preset input", () => {
  const presetInput = {
    type: "preset",
    config: {
      case: "presetInput",
      value: {
        base: { description: "Preset", ui: "" },
        tags: ["agent", "model"],
        multi: true,
      },
    },
  };

  it("getInputTags", () => {
    expect(getInputTags(presetInput)).toEqual(["agent", "model"]);
  });

  it("getInputTag returns comma-separated", () => {
    expect(getInputTag(presetInput)).toBe("agent,model");
  });

  it("getInputMulti for preset", () => {
    expect(getInputMulti(presetInput)).toBe(true);
  });
});

describe("isConfigurableInput", () => {
  it("returns true for normal inputs", () => {
    expect(isConfigurableInput({ type: "string", config: { case: "stringInput", value: { base: { ui: "config" } } } })).toBe(true);
    expect(isConfigurableInput({ type: "enum", config: { case: "enumInput", value: { base: { ui: "toolbar" } } } })).toBe(true);
  });

  it("returns false for hidden inputs", () => {
    expect(isConfigurableInput({ type: "string", config: { case: "stringInput", value: { base: { ui: "hidden" } } } })).toBe(false);
  });

  it("returns false for message/attachments/preset/thread types", () => {
    expect(isConfigurableInput({ type: "message" })).toBe(false);
    expect(isConfigurableInput({ type: "attachments" })).toBe(false);
    expect(isConfigurableInput({ type: "preset" })).toBe(false);
    expect(isConfigurableInput({ type: "thread" })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Setters
// ---------------------------------------------------------------------------

describe("setters — immutable updates", () => {
  const input = {
    type: "enum",
    config: {
      case: "enumInput",
      value: {
        base: { description: "Mode", ui: "toolbar" },
        enumValues: ["a", "b"],
        multi: false,
      },
    },
  };

  it("setInputDescription returns new object", () => {
    const updated = setInputDescription(input, "New desc");
    expect(getInputDescription(updated)).toBe("New desc");
    expect(getInputDescription(input)).toBe("Mode"); // original unchanged
  });

  it("setInputUI", () => {
    const updated = setInputUI(input, "config");
    expect(getInputUI(updated)).toBe("config");
  });

  it("setInputDefault", () => {
    const updated = setInputDefault(input, "new-default");
    expect(getInputDefault(updated)).toBe("new-default");
    expect(getInputDefault(input)).toBeUndefined(); // original unchanged
  });

  it("setInputEnumValues", () => {
    const updated = setInputEnumValues(input, ["x", "y", "z"]);
    expect(getInputEnumValues(updated)).toEqual(["x", "y", "z"]);
  });

  it("setInputMulti", () => {
    const updated = setInputMulti(input, true);
    expect(getInputMulti(updated)).toBe(true);
  });
});

describe("setters — min/max maps to correct field", () => {
  it("setInputMin on string → minLength", () => {
    const str = createInput("string");
    const updated = setInputMin(str, 5);
    expect(updated.config.value.minLength).toBe(5);
  });

  it("setInputMax on string → maxLength", () => {
    const str = createInput("string");
    const updated = setInputMax(str, 100);
    expect(updated.config.value.maxLength).toBe(100);
  });

  it("setInputMin on integer → min", () => {
    const int = createInput("integer");
    const updated = setInputMin(int, 0);
    expect(updated.config.value.min).toBe(0);
  });

  it("setInputMin on array → minItems", () => {
    const arr = createInput("array");
    const updated = setInputMin(arr, 1);
    expect(updated.config.value.minItems).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// createInput
// ---------------------------------------------------------------------------

describe("createInput", () => {
  it("creates string input with correct config case", () => {
    const input = createInput("string");
    expect(input.type).toBe("string");
    expect(input.config.case).toBe("stringInput");
    expect(input.config.value.base).toBeDefined();
  });

  it("creates enum input with empty enumValues", () => {
    const input = createInput("enum");
    expect(input.config.case).toBe("enumInput");
    expect(input.config.value.enumValues).toEqual([]);
    expect(input.config.value.multi).toBe(false);
  });

  it("creates group input with empty inputs map", () => {
    const input = createInput("group");
    expect(input.config.case).toBe("groupInput");
    expect(input.config.value.inputs).toEqual({});
  });

  it("applies init values", () => {
    const input = createInput("enum", {
      description: "Pick one",
      ui: "toolbar",
      enumValues: ["a", "b"],
    });
    expect(getInputDescription(input)).toBe("Pick one");
    expect(getInputUI(input)).toBe("toolbar");
    expect(getInputEnumValues(input)).toEqual(["a", "b"]);
  });
});


// ---------------------------------------------------------------------------
// example — what a value looks like, shown in the Run form's empty box
// ---------------------------------------------------------------------------

describe("example", () => {
  it("reads and writes the example on the input's base", () => {
    const input = setInputExample(createInput("string", { description: "Slack channel" }), "C0123ABCDEF");
    expect(getInputExample(input)).toBe("C0123ABCDEF");
    // The rest of the base is kept.
    expect(getInputDescription(input)).toBe("Slack channel");
  });

  it("is undefined when unset", () => {
    expect(getInputExample(createInput("string"))).toBeUndefined();
  });

  it("is set by createInput and the params editor's update path", () => {
    expect(getInputExample(createInput("integer", { example: "42" }))).toBe("42");
    const updated = applyInputUpdates(createInput("string"), { description: "Repo", example: "acme/app" });
    expect(getInputExample(updated)).toBe("acme/app");
    expect(getInputDescription(updated)).toBe("Repo");
  });

  it("survives a change of type, as the description does", () => {
    const changed = changeInputType(createInput("string", { description: "Count", example: "3" }), "integer");
    expect(changed.config.case).toBe("integerInput");
    expect(getInputExample(changed)).toBe("3");
    expect(getInputDescription(changed)).toBe("Count");
  });
});

describe("changeInputType keeps what lives outside the config", () => {
  it("keeps fields a caller stores on the input, like the params editor's name and key", () => {
    const named = { ...createInput("string", { description: "Retries" }), _id: "param-1", _name: "max_attempts" };
    const changed = changeInputType(named, "integer") as typeof named;
    expect(changed._name).toBe("max_attempts");
    expect(changed._id).toBe("param-1");
    expect(changed.type).toBe("integer");
    expect(changed.config.case).toBe("integerInput");
    expect(getInputDescription(changed)).toBe("Retries");
  });

  it("keeps them through applyInputUpdates, the editor's update path", () => {
    const named = { ...createInput("string"), _id: "param-1", _name: "max_attempts" };
    const changed = applyInputUpdates(named, { type: "enum", default: undefined }) as typeof named;
    expect(changed._name).toBe("max_attempts");
    expect(changed._id).toBe("param-1");
    expect(changed.config.case).toBe("enumInput");
  });
});

describe("setInputDefault writes each config case's wire type", () => {
  // `default` is a string, double, int64, bool, ModelSelector or
  // google.protobuf.Value depending on the config case. A value in the wrong
  // shape is accepted here and only throws when the save request is encoded,
  // so each case goes through the same proto JSON encode/decode a save does.
  function saveAndReload(input: InputDef): InputDef {
    const json = toJson(InputSchema, create(InputSchema, input as MessageInitShape<typeof InputSchema>));
    return fromJson(InputSchema, json);
  }

  const cases: Array<[type: string, value: unknown, reloaded: unknown]> = [
    ["string", "main", "main"],
    ["message", "Fix the flaky test", "Fix the flaky test"],
    ["number", 0.75, 0.75],
    ["number", "0.75", 0.75],
    ["integer", 5, 5],
    ["integer", "5", 5],
    ["integer", 5n, 5],
    ["boolean", true, true],
    ["boolean", false, false],
    ["enum", "b", "b"],
    ["model", "claude-sonnet-5", expect.objectContaining({ id: "claude-sonnet-5" })],
    ["model", { tags: ["flagship"] }, expect.objectContaining({ tags: ["flagship"] })],
    ["tools", ["bash", "read"], ["bash", "read"]],
    ["attachments", ["spec.md"], ["spec.md"]],
    ["array", [1, 2], [1, 2]],
    ["object", { retries: 2 }, { retries: 2 }],
    ["any", "anything", "anything"],
    ["preset", ["careful"], ["careful"]],
  ];

  it.each(cases)("%s default %o survives save and reload", (type, value, reloaded) => {
    const input = setInputDefault(createInput(type), value);
    expect(getInputDefault(input)).toEqual(reloaded);
    expect(getInputDefault(saveAndReload(input))).toEqual(reloaded);
  });

  it("stores an integer default as an exact int64", () => {
    const big = "9007199254740993"; // 2^53 + 1: a JS number rounds it
    const input = setInputDefault(createInput("integer"), big);
    expect(getInputIntegerDefault(input)).toBe(9007199254740993n);
    expect(getInputIntegerDefault(saveAndReload(input))).toBe(9007199254740993n);
  });

  it("clears the default for undefined", () => {
    for (const type of ["string", "number", "integer", "boolean", "enum", "model", "object"]) {
      const input = setInputDefault(setInputDefault(createInput(type), type === "boolean" ? true : "1"), undefined);
      expect(getInputDefault(input), type).toBeUndefined();
      expect(getInputDefault(saveAndReload(input)), type).toBeUndefined();
    }
  });

  it("drops a value the field cannot hold rather than writing an unencodable one", () => {
    expect(getInputDefault(setInputDefault(createInput("integer"), "abc"))).toBeUndefined();
    expect(getInputDefault(setInputDefault(createInput("number"), "abc"))).toBeUndefined();
    expect(getInputDefault(setInputDefault(createInput("boolean"), "maybe"))).toBeUndefined();
    expect(getInputDefault(setInputDefault(createInput("integer"), "99999999999999999999"))).toBeUndefined();
  });
});

describe("isInternalInput", () => {
  it("is true for hidden inputs and the ones a workflow wires itself", () => {
    expect(isInternalInput(createInput("string", { ui: "hidden" }))).toBe(true);
    expect(isInternalInput(createInput("preset"))).toBe(true);
  });

  it("is false for inputs a person or an event fills", () => {
    expect(isInternalInput(createInput("string"))).toBe(false);
    expect(isInternalInput(createInput("model", { ui: "toolbar" }))).toBe(false);
  });
});
