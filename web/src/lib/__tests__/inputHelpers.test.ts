// Copyright (c) 2025 Reliant Labs

import { describe, it, expect } from "vitest";
import {
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
    expect(updated.config.value.default).toBe("new-default");
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

